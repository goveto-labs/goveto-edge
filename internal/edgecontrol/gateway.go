package edgecontrol

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/stdlib"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	_ "google.golang.org/grpc/encoding/gzip"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"goveto-edge/internal/edgeprotocol"
	"goveto-edge/internal/storage/gen/client"
)

const (
	heartbeatInterval      = 10 * time.Second
	heartbeatTimeout       = 45 * time.Second
	taskLease              = 45 * time.Second
	taskSweepInterval      = 30 * time.Second
	agentTaskTimeout       = 15 * time.Minute
	maxInflightTasks       = 16
	maxLogBatchRecords     = 2000
	maxLogBatchBytes       = 4 << 20
	rotateBefore           = 7 * 24 * time.Hour
	completedTaskTTL       = 7 * 24 * time.Hour
	dispatchCleanupTimeout = 5 * time.Second
	gatewayEventTopic      = "goveto_edge_gateway_events"
)

const cancelAbandonedTaskSQL = `UPDATE agent_tasks SET status='CANCELLED',
	cancel_requested_at=NOW(), error=COALESCE(error, 'dispatch caller stopped waiting'),
	lease_owner=NULL, lease_until=NULL, heartbeat_at=NULL, updated_at=NOW()
	WHERE id=$1 AND status IN ('PENDING','RUNNING')`

const dispatchTaskSQL = `INSERT INTO agent_tasks
	(id, node_id, kind, payload, status, idempotency_key, timeout_at, created_at, updated_at)
	VALUES ($1, $2, $3, $4, 'PENDING', $6,
		NOW()+($5*INTERVAL '1 second'), NOW(), NOW())`

const claimTasksSQL = `WITH picked AS (
	SELECT t.id FROM agent_tasks t JOIN nodes n ON n.id = t.node_id
	JOIN node_credentials c ON c.node_id = n.id
	WHERE t.node_id = $1 AND n.status = 'ONLINE' AND c.revoked_at IS NULL
	AND t.attempts < t.max_attempts AND t.cancel_requested_at IS NULL
	AND (t.timeout_at IS NULL OR t.timeout_at > NOW()) AND (
		(t.status = 'PENDING' AND t.next_attempt_at <= NOW()) OR
		(t.status = 'RUNNING' AND (t.lease_until IS NULL OR t.lease_until < NOW())))
	ORDER BY CASE WHEN t.kind = $5 THEN 0 ELSE 1 END, t.created_at
	FOR UPDATE OF t SKIP LOCKED LIMIT $2)
	UPDATE agent_tasks t SET status = 'RUNNING', lease_owner = $3,
	lease_until = NOW() + ($4 * INTERVAL '1 second'), heartbeat_at=NOW(),
	attempts = attempts + 1, updated_at = NOW()
	FROM picked WHERE t.id = picked.id RETURNING t.id, t.kind, t.payload`

var errInvalidCredentialCSR = errors.New("invalid credential CSR")

type LogConsumer func(context.Context, string, []edgeprotocol.LogRecord) error

type ApplySiteResult struct {
	SiteID        string `json:"site_id"`
	Version       uint64 `json:"version"`
	ConfigVersion uint64 `json:"config_version"`
	Applied       bool   `json:"applied"`
}

type dispatchTaskState struct {
	Status string           `db:"status"`
	Result *json.RawMessage `db:"result_json"`
	Error  *string          `db:"error"`
}

type Gateway struct {
	sqlDB          *sql.DB
	db             *client.Client
	authority      *Authority
	instanceID     string
	consumeLogs    LogConsumer
	onStatusChange func(context.Context, string)
	mu             sync.Mutex
	sessions       map[string]*session
	geoIP          *geoIPAsset
}

type session struct {
	cancel   context.CancelFunc
	wake     chan struct{}
	owner    string
	logQueue agentLogQueueState
}

type agentLogQueueState struct {
	nonEmptySince time.Time
	lastWarning   time.Time
	records       uint64
	bytes         uint64
	dropped       uint64
}

type gatewayEvent struct {
	Source string `json:"source"`
	Kind   string `json:"kind"`
	NodeID string `json:"node_id"`
}

func NewGateway(
	sqlDB *sql.DB,
	db *client.Client,
	authority *Authority,
	consumeLogs LogConsumer,
	onStatusChange func(context.Context, string),
) *Gateway {
	return &Gateway{
		sqlDB: sqlDB, db: db, authority: authority, instanceID: uuid.NewString(), consumeLogs: consumeLogs,
		onStatusChange: onStatusChange, sessions: map[string]*session{},
	}
}

func (g *Gateway) Run(ctx context.Context) {
	go g.listenForEvents(ctx)
	go g.runGeoIP(ctx)
	g.runTaskSweep(ctx)
	g.cleanupTasks(ctx)
	sweepTicker := time.NewTicker(taskSweepInterval)
	cleanupTicker := time.NewTicker(time.Hour)
	defer sweepTicker.Stop()
	defer cleanupTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sweepTicker.C:
			g.runTaskSweep(ctx)
		case <-cleanupTicker.C:
			g.cleanupTasks(ctx)
		}
	}
}

func (g *Gateway) runTaskSweep(ctx context.Context) {
	if err := g.convergeTasks(ctx); err != nil && ctx.Err() == nil {
		slog.Warn("converge agent tasks", "error", err)
	}
}

func (g *Gateway) Connect(stream edgeprotocol.ManagementConnectServer) error {
	certificate, err := peerCertificate(stream.Context())
	if err != nil {
		return status.Error(codes.Unauthenticated, err.Error())
	}
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	if first.Hello == nil || first.Hello.NodeID == "" {
		return status.Error(codes.InvalidArgument, "the first frame must be an agent hello")
	}
	nodeID := first.Hello.NodeID
	if CertificateNodeID(certificate) != nodeID {
		return status.Error(codes.Unauthenticated, "certificate identity does not match node")
	}
	if err := g.authorize(stream.Context(), nodeID, certificate); err != nil {
		return status.Error(codes.PermissionDenied, err.Error())
	}

	ctx, cancel := context.WithCancel(stream.Context())
	current := &session{cancel: cancel, wake: make(chan struct{}, 1), owner: g.instanceID + "/" + uuid.NewString()}
	g.register(nodeID, current)
	defer func() {
		cancel()
		g.unregister(nodeID, current)
		g.releaseLeases(context.Background(), nodeID, current.owner)
	}()

	clusterID, wasOnline, err := g.recordHeartbeat(
		ctx, nodeID, first.Hello.CacheConfig, first.Hello.SiteVersions, first.Hello.AgentVersion,
	)
	if err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	if !wasOnline {
		g.notifyStatusChange(ctx, clusterID)
	}
	g.ensureGeoIPTask(ctx, nodeID, first.Hello.GeoIP)
	if err := stream.Send(&edgeprotocol.ServerMessage{Welcome: &edgeprotocol.ServerWelcome{
		HeartbeatSeconds: int(heartbeatInterval.Seconds()), MaxInflightTasks: maxInflightTasks,
		RotateBeforeHours: int(rotateBefore.Hours()), MaxLogBatchRecords: maxLogBatchRecords,
		MaxLogBatchBytes: maxLogBatchBytes,
	}}); err != nil {
		return err
	}
	rotateRequested := time.Until(certificate.NotAfter) <= rotateBefore
	if rotateRequested {
		if err := stream.Send(&edgeprotocol.ServerMessage{RotateCredential: true}); err != nil {
			return err
		}
	}

	received := make(chan receiveResult, 1)
	go receiveLoop(stream, received)
	logResults := make(chan logConsumeResult, 1)
	logInFlight := false
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	deadline := time.NewTimer(heartbeatTimeout)
	defer deadline.Stop()
	inflight := map[string]struct{}{}

	for {
		if len(inflight) < maxInflightTasks {
			tasks, claimErr := g.claimTasks(ctx, nodeID, current.owner, maxInflightTasks-len(inflight))
			if claimErr != nil {
				slog.Warn("claim agent tasks", "node_id", nodeID, "error", claimErr)
			} else {
				for _, task := range tasks {
					if err := stream.Send(&edgeprotocol.ServerMessage{Task: &task}); err != nil {
						return err
					}
					inflight[task.ID] = struct{}{}
				}
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := g.authorize(ctx, nodeID, certificate); err != nil {
				return status.Error(codes.PermissionDenied, err.Error())
			}
			if len(inflight) > 0 {
				g.renewLeases(ctx, current.owner)
			}
			if !rotateRequested && time.Until(certificate.NotAfter) <= rotateBefore {
				if err := stream.Send(&edgeprotocol.ServerMessage{RotateCredential: true}); err != nil {
					return err
				}
				rotateRequested = true
			}
		case <-current.wake:
		case <-deadline.C:
			return status.Error(codes.DeadlineExceeded, "agent heartbeat timed out")
		case result := <-logResults:
			logInFlight = false
			if result.err != nil {
				slog.Warn("consume agent logs", "node_id", nodeID, "error", result.err)
				ack := edgeprotocol.AgentLogAck{
					Accepted: false, RetryAfterMS: 1000, Error: "ingest unavailable",
				}
				if err := stream.Send(&edgeprotocol.ServerMessage{LogsAck: &ack}); err != nil {
					return err
				}
				break
			}
			if err := stream.Send(acceptedLogAck(result.through)); err != nil {
				return err
			}
		case receivedFrame := <-received:
			if receivedFrame.err != nil {
				if errors.Is(receivedFrame.err, io.EOF) {
					return nil
				}
				return receivedFrame.err
			}
			message := receivedFrame.message
			switch {
			case message.Heartbeat != nil:
				resetTimer(deadline, heartbeatTimeout)
				if err := g.authorize(ctx, nodeID, certificate); err != nil {
					return status.Error(codes.PermissionDenied, err.Error())
				}
				if _, _, err := g.recordHeartbeat(
					ctx, nodeID, message.Heartbeat.CacheConfig, message.Heartbeat.SiteVersions, "",
				); err != nil {
					return status.Error(codes.Internal, err.Error())
				}
				g.observeAgentLogQueue(nodeID, current, *message.Heartbeat)
				g.ensureGeoIPTask(ctx, nodeID, message.Heartbeat.GeoIP)
			case message.TaskResult != nil:
				delete(inflight, message.TaskResult.TaskID)
				if err := g.completeTask(ctx, nodeID, current.owner, *message.TaskResult); err != nil {
					return status.Error(codes.Internal, err.Error())
				}
			case message.Logs != nil:
				if logInFlight {
					return status.Error(codes.FailedPrecondition, "an agent log batch is already being ingested")
				}
				if g.consumeLogs == nil {
					ack := edgeprotocol.AgentLogAck{
						Accepted: false, RetryAfterMS: 30000, Error: "analytics ingest is disabled",
					}
					if err := stream.Send(&edgeprotocol.ServerMessage{LogsAck: &ack}); err != nil {
						return err
					}
					break
				}
				if err := validateLogBatch(*message.Logs); err != nil {
					ack := edgeprotocol.AgentLogAck{
						Accepted: false, RetryAfterMS: 5000, Error: err.Error(),
					}
					if sendErr := stream.Send(&edgeprotocol.ServerMessage{LogsAck: &ack}); sendErr != nil {
						return sendErr
					}
					break
				}
				if len(message.Logs.Records) == 0 {
					if err := stream.Send(acceptedLogAck(0)); err != nil {
						return err
					}
					break
				}
				logInFlight = true
				batch := *message.Logs
				go func() {
					consumeErr := g.consumeLogs(ctx, nodeID, batch.Records)
					select {
					case logResults <- logConsumeResult{through: batch.Through, err: consumeErr}:
					case <-ctx.Done():
					}
				}()
			case message.CredentialRequest != nil:
				certificatePEM, _, notAfter, err := g.rotateCredential(
					ctx, nodeID, certificate.SerialNumber.Text(16), message.CredentialRequest.CSRPEM,
				)
				if err != nil {
					if errors.Is(err, errInvalidCredentialCSR) {
						return status.Error(codes.InvalidArgument, err.Error())
					}
					return status.Error(codes.Internal, err.Error())
				}
				if err := stream.Send(&edgeprotocol.ServerMessage{Credential: &edgeprotocol.CredentialUpdate{
					CertificatePEM: certificatePEM, NotAfter: notAfter.Format(time.RFC3339),
				}}); err != nil {
					return err
				}
			}
		}
	}
}

func acceptedLogAck(through uint64) *edgeprotocol.ServerMessage {
	ack := edgeprotocol.AgentLogAck{Through: through, Accepted: true}
	return &edgeprotocol.ServerMessage{LogsAckThrough: &through, LogsAck: &ack}
}

func validateLogBatch(batch edgeprotocol.AgentLogBatch) error {
	if len(batch.Records) == 0 {
		if batch.Through != 0 || batch.FirstID != 0 {
			return errors.New("empty log batch has a cursor")
		}
		return nil
	}
	if len(batch.Records) > maxLogBatchRecords || batch.Bytes > maxLogBatchBytes {
		return errors.New("log batch exceeds gateway limits")
	}
	first := batch.Records[0].ID
	last := batch.Records[len(batch.Records)-1].ID
	if first == 0 {
		return errors.New("log batch record IDs must be positive")
	}
	if batch.FirstID != 0 && batch.FirstID != first || batch.Through != last {
		return errors.New("log batch cursor does not match records")
	}
	var actualBytes uint64
	previous := first
	for index, record := range batch.Records {
		encoded, err := json.Marshal(record)
		if err != nil {
			return fmt.Errorf("encode log batch record: %w", err)
		}
		actualBytes += uint64(len(encoded))
		if actualBytes > maxLogBatchBytes {
			return errors.New("log batch exceeds gateway limits")
		}
		if index == 0 {
			continue
		}
		if record.ID <= previous {
			return errors.New("log batch record IDs are not increasing")
		}
		previous = record.ID
	}
	return nil
}

type receiveResult struct {
	message *edgeprotocol.ClientMessage
	err     error
}

type logConsumeResult struct {
	through uint64
	err     error
}

func receiveLoop(stream edgeprotocol.ManagementConnectServer, target chan<- receiveResult) {
	for {
		message, err := stream.Recv()
		select {
		case target <- receiveResult{message: message, err: err}:
		case <-stream.Context().Done():
			return
		}
		if err != nil {
			return
		}
	}
}

func (g *Gateway) renewLeases(ctx context.Context, owner string) {
	if _, err := g.db.RawExec(ctx, `UPDATE agent_tasks SET lease_until = NOW() + ($2 * INTERVAL '1 second'),
		heartbeat_at = NOW(), updated_at = NOW() WHERE lease_owner = $1 AND status = 'RUNNING'
		AND cancel_requested_at IS NULL AND (timeout_at IS NULL OR timeout_at > NOW())
		AND EXISTS (SELECT 1 FROM nodes n JOIN node_credentials c ON c.node_id = n.id
			WHERE n.id = agent_tasks.node_id AND n.status = 'ONLINE' AND c.revoked_at IS NULL)`,
		owner, taskLease.Seconds()); err != nil {
		slog.Warn("renew agent task leases", "owner", owner, "error", err)
	}
}

func peerCertificate(ctx context.Context) (*x509.Certificate, error) {
	remote, ok := peer.FromContext(ctx)
	if !ok {
		return nil, errors.New("missing mTLS peer")
	}
	info, ok := remote.AuthInfo.(credentials.TLSInfo)
	if !ok || len(info.State.PeerCertificates) == 0 {
		return nil, errors.New("missing client certificate")
	}
	return info.State.PeerCertificates[0], nil
}

func (g *Gateway) authorize(ctx context.Context, nodeID string, certificate *x509.Certificate) error {
	type credentialState struct {
		Serial             *string    `db:"certificate_serial"`
		PreviousSerial     *string    `db:"previous_certificate_serial"`
		PreviousValidUntil *time.Time `db:"previous_certificate_valid_until"`
		RevokedAt          *time.Time `db:"revoked_at"`
	}
	rows, err := client.Raw[credentialState](ctx, g.db, `SELECT c.certificate_serial,
		c.previous_certificate_serial, c.previous_certificate_valid_until, c.revoked_at
		FROM node_credentials c JOIN nodes n ON n.id = c.node_id
		WHERE c.node_id = $1 AND n.status <> 'DISABLED'`, nodeID)
	if err != nil || len(rows) != 1 {
		return errors.New("node credential is not registered")
	}
	if rows[0].RevokedAt != nil {
		return errors.New("node credential is revoked")
	}
	serial := certificate.SerialNumber.Text(16)
	current := rows[0].Serial != nil && *rows[0].Serial == serial
	previous := rows[0].PreviousSerial != nil && *rows[0].PreviousSerial == serial &&
		rows[0].PreviousValidUntil != nil && time.Now().Before(*rows[0].PreviousValidUntil)
	if !current && !previous {
		return errors.New("node certificate has been rotated or revoked")
	}
	return nil
}

func (g *Gateway) recordHeartbeat(
	ctx context.Context,
	nodeID string,
	current edgeprotocol.NodeCacheConfig,
	siteVersions map[string]uint64,
	agentVersion string,
) (string, bool, error) {
	type nodeState struct {
		ClusterID string `db:"cluster_id"`
		Status    string `db:"status"`
	}
	var state nodeState
	err := g.db.Tx(ctx, func(tx *client.Client) error {
		type credentialLock struct {
			NodeID string `db:"node_id"`
		}
		credentials, err := client.Raw[credentialLock](ctx, tx, `SELECT node_id FROM node_credentials
			WHERE node_id = $1 FOR UPDATE`, nodeID)
		if err != nil {
			return err
		}
		if len(credentials) != 1 {
			return errors.New("node credential is not registered")
		}
		rows, err := client.Raw[nodeState](ctx, tx, `WITH candidate AS (
			SELECT id, cluster_id, status FROM nodes
			WHERE id = $1 AND status <> 'DISABLED' FOR UPDATE
		)
		UPDATE nodes n SET status = 'ONLINE', heartbeat_at = NOW(), install_error = NULL,
			version = CASE WHEN $2 <> '' THEN $2 ELSE n.version END, updated_at = NOW()
		FROM candidate c WHERE n.id = c.id RETURNING c.cluster_id, c.status`, nodeID, agentVersion)
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			return errors.New("node is disabled or unavailable")
		}
		state = rows[0]
		_, err = tx.RawExec(ctx, `UPDATE node_credentials SET bootstrap_identity_encrypted = NULL
			WHERE node_id = $1 AND bootstrap_identity_encrypted IS NOT NULL`, nodeID)
		return err
	})
	if err != nil {
		return "", false, fmt.Errorf("record node heartbeat: %w", err)
	}
	if err := g.reconcileCacheConfig(ctx, nodeID, current); err != nil {
		slog.Warn("reconcile cache config from heartbeat", "node_id", nodeID, "error", err)
	}
	if err := g.reconcileSiteVersions(ctx, nodeID, state.ClusterID, siteVersions); err != nil {
		slog.Warn("reconcile site configs from heartbeat", "node_id", nodeID, "error", err)
	}
	return state.ClusterID, state.Status == "ONLINE", nil
}

func (g *Gateway) observeAgentLogQueue(nodeID string, current *session, heartbeat edgeprotocol.AgentHeartbeat) {
	now := time.Now()
	state := &current.logQueue
	if heartbeat.DroppedLogs > state.dropped {
		slog.Warn("agent dropped queued logs", "node_id", nodeID,
			"dropped_total", heartbeat.DroppedLogs, "dropped_since_last", heartbeat.DroppedLogs-state.dropped)
	}
	state.dropped = heartbeat.DroppedLogs
	state.records = heartbeat.QueueRecords
	state.bytes = heartbeat.QueueBytes
	if heartbeat.QueueRecords == 0 {
		if !state.nonEmptySince.IsZero() && now.Sub(state.nonEmptySince) >= time.Minute {
			slog.Info("agent log queue drained", "node_id", nodeID)
		}
		state.nonEmptySince = time.Time{}
		state.lastWarning = time.Time{}
		return
	}
	if state.nonEmptySince.IsZero() {
		state.nonEmptySince = now
		return
	}
	if now.Sub(state.nonEmptySince) >= time.Minute &&
		(state.lastWarning.IsZero() || now.Sub(state.lastWarning) >= time.Minute) {
		slog.Warn("agent log queue remains backlogged", "node_id", nodeID,
			"queue_records", heartbeat.QueueRecords, "queue_bytes", heartbeat.QueueBytes,
			"backlog_duration", now.Sub(state.nonEmptySince).Round(time.Second))
		state.lastWarning = now
	}
}

func (g *Gateway) reconcileCacheConfig(ctx context.Context, nodeID string, current edgeprotocol.NodeCacheConfig) error {
	type storedConfig struct {
		CacheDirectory      string `db:"cache_dir"`
		AutoMaxSize         bool   `db:"auto_max_size"`
		MaxSizeBytes        *int64 `db:"max_size_bytes"`
		MaxDiskUsagePercent int    `db:"max_disk_usage_percent"`
	}
	rows, err := client.Raw[storedConfig](ctx, g.db, `SELECT cache_dir, auto_max_size, max_size_bytes,
		max_disk_usage_percent FROM node_cache_configs WHERE node_id = $1`, nodeID)
	if err != nil || len(rows) != 1 {
		return err
	}
	desired := edgeprotocol.NodeCacheConfig{
		CacheDirectory: rows[0].CacheDirectory, AutoMaxSize: rows[0].AutoMaxSize,
		MaxDiskUsagePercent: rows[0].MaxDiskUsagePercent,
	}
	if rows[0].MaxSizeBytes != nil {
		desired.MaxSizeBytes = uint64(*rows[0].MaxSizeBytes)
	}
	if desired == current {
		return nil
	}
	payload, _ := json.Marshal(desired)
	var inserted int64
	err = g.db.Tx(ctx, func(tx *client.Client) error {
		type dispatchTarget struct {
			NodeID string `db:"node_id"`
		}
		targets, err := client.Raw[dispatchTarget](ctx, tx, `SELECT c.node_id
			FROM node_credentials c JOIN nodes n ON n.id = c.node_id
			WHERE c.node_id = $1 AND n.status = 'ONLINE' AND c.revoked_at IS NULL
			FOR UPDATE OF c, n`, nodeID)
		if err != nil || len(targets) != 1 {
			return err
		}
		inserted, err = tx.RawExec(ctx, `INSERT INTO agent_tasks
			(id, node_id, kind, payload, status, timeout_at, created_at, updated_at)
			SELECT $1, $2, $3, $4, 'PENDING', NOW()+($5*INTERVAL '1 second'), NOW(), NOW()
			WHERE NOT EXISTS (SELECT 1 FROM agent_tasks WHERE node_id = $2 AND kind = $3
			AND status IN ('PENDING', 'RUNNING'))`, uuid.NewString(), nodeID,
			edgeprotocol.TaskNodeCacheConfig, payload, agentTaskTimeout.Seconds())
		return err
	})
	if err == nil && inserted == 1 {
		g.signal(ctx, "wake", nodeID)
	}
	return err
}

func (g *Gateway) Dispatch(ctx context.Context, nodeID, kind string, payload, result any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	taskID := uuid.NewString()
	err = g.db.Tx(ctx, func(tx *client.Client) error {
		type dispatchTarget struct {
			NodeID string `db:"node_id"`
		}
		rows, err := client.Raw[dispatchTarget](ctx, tx, `SELECT c.node_id
			FROM node_credentials c JOIN nodes n ON n.id = c.node_id
			WHERE c.node_id = $1 AND n.status <> 'DISABLED' AND c.revoked_at IS NULL
			FOR UPDATE OF c, n`, nodeID)
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			return errors.New("node is disabled, revoked, or unavailable")
		}
		_, err = tx.RawExec(ctx, dispatchTaskSQL,
			taskID, nodeID, kind, encoded, agentTaskTimeout.Seconds(), taskID)
		return err
	})
	if err != nil {
		return err
	}
	g.signal(ctx, "wake", nodeID)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		rows, queryErr := client.Raw[dispatchTaskState](ctx, g.db, `SELECT status, result_json, error FROM agent_tasks WHERE id = $1`, taskID)
		if queryErr != nil {
			if ctx.Err() != nil {
				return g.cancelAbandonedTask(ctx, taskID)
			}
			return queryErr
		}
		if len(rows) == 1 {
			switch rows[0].Status {
			case "SUCCEEDED":
				if result != nil && rows[0].Result != nil && len(*rows[0].Result) > 0 {
					return json.Unmarshal(*rows[0].Result, result)
				}
				return nil
			case "FAILED", "DEAD_LETTER", "CANCELLED":
				if rows[0].Error != nil {
					return errors.New(*rows[0].Error)
				}
				return fmt.Errorf("agent task %s", rows[0].Status)
			}
		}
		select {
		case <-ctx.Done():
			return g.cancelAbandonedTask(ctx, taskID)
		case <-ticker.C:
		}
	}
}

func (g *Gateway) cancelAbandonedTask(ctx context.Context, taskID string) error {
	waitErr := ctx.Err()
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dispatchCleanupTimeout)
	defer cancel()
	_, cleanupErr := g.db.RawExec(cleanupCtx, cancelAbandonedTaskSQL, taskID)
	if cleanupErr != nil {
		return errors.Join(waitErr, fmt.Errorf("cancel abandoned agent task: %w", cleanupErr))
	}
	return waitErr
}

func (g *Gateway) claimTasks(ctx context.Context, nodeID, owner string, limit int) ([]edgeprotocol.AgentTask, error) {
	type claimedTask struct {
		ID      string          `db:"id"`
		Kind    string          `db:"kind"`
		Payload json.RawMessage `db:"payload"`
	}
	rows, err := client.Raw[claimedTask](ctx, g.db, claimTasksSQL,
		nodeID, limit, owner, taskLease.Seconds(), edgeprotocol.TaskSyncGeoIP)
	if err != nil {
		return nil, err
	}
	tasks := make([]edgeprotocol.AgentTask, 0, len(rows))
	for _, row := range rows {
		tasks = append(tasks, edgeprotocol.AgentTask{ID: row.ID, Kind: row.Kind, Payload: row.Payload})
	}
	return tasks, nil
}

func (g *Gateway) completeTask(ctx context.Context, nodeID, owner string, result edgeprotocol.AgentTaskResult) error {
	statusValue := "SUCCEEDED"
	if !result.Success {
		statusValue = "FAILED"
	}
	affected, err := g.db.RawExec(ctx, `UPDATE agent_tasks SET status=$4,
		result_json=$5, error=$6, lease_owner=NULL, lease_until=NULL, heartbeat_at=NULL, updated_at=NOW()
		WHERE id = $1 AND node_id = $2 AND lease_owner = $3 AND status = 'RUNNING'
		AND EXISTS (SELECT 1 FROM nodes n JOIN node_credentials c ON c.node_id = n.id
			WHERE n.id = $2 AND n.status = 'ONLINE' AND c.revoked_at IS NULL)`,
		result.TaskID, nodeID, owner, statusValue, nullableJSON(result.Result), nullableString(result.Error))
	if err != nil {
		return err
	}
	if affected != 1 {
		type taskState struct {
			Status string `db:"status"`
		}
		rows, queryErr := client.Raw[taskState](ctx, g.db,
			"SELECT status FROM agent_tasks WHERE id=$1 AND node_id=$2", result.TaskID, nodeID)
		if queryErr == nil && len(rows) == 1 {
			switch rows[0].Status {
			case "SUCCEEDED", "FAILED", "DEAD_LETTER", "CANCELLED":
				// The task already converged while this result was in flight.
				return nil
			}
		}
		return fmt.Errorf("agent task %s lease is stale or owned by another session", result.TaskID)
	}
	return nil
}

func (g *Gateway) convergeTasks(ctx context.Context) error {
	type lockRow struct {
		Locked bool `db:"locked"`
	}
	return g.db.Tx(ctx, func(tx *client.Client) error {
		locks, err := client.Raw[lockRow](ctx, tx,
			"SELECT pg_try_advisory_xact_lock(hashtext($1)) AS locked", "agent-task-sweep")
		if err != nil || len(locks) != 1 || !locks[0].Locked {
			return err
		}
		if _, err := tx.RawExec(ctx, `UPDATE agent_tasks SET status='CANCELLED',
			error=COALESCE(error, 'agent task cancellation requested'), lease_owner=NULL,
			lease_until=NULL, heartbeat_at=NULL, updated_at=NOW()
			WHERE cancel_requested_at IS NOT NULL
			AND (status='PENDING' OR (status='RUNNING' AND
			(lease_until IS NULL OR lease_until<=NOW())))`); err != nil {
			return err
		}
		if _, err := tx.RawExec(ctx, `UPDATE agent_tasks SET status='DEAD_LETTER',
			error=COALESCE(error, 'agent task execution deadline expired'), lease_owner=NULL,
			lease_until=NULL, heartbeat_at=NULL, updated_at=NOW()
			WHERE timeout_at<=NOW()
			AND (status='PENDING' OR (status='RUNNING' AND
			(lease_until IS NULL OR lease_until<=NOW())))`); err != nil {
			return err
		}
		_, err = tx.RawExec(ctx, `UPDATE agent_tasks SET status='DEAD_LETTER',
			error=COALESCE(error, 'maximum agent task delivery attempts exceeded'), lease_owner=NULL,
			lease_until=NULL, heartbeat_at=NULL, updated_at=NOW()
			WHERE attempts>=max_attempts AND (
			status='PENDING' OR (status='RUNNING' AND
			(lease_until IS NULL OR lease_until<=NOW())))`)
		return err
	})
}

func (g *Gateway) rotateCredential(
	ctx context.Context,
	nodeID, connectionSerial, csrPEM string,
) (string, string, time.Time, error) {
	candidatePEM, candidateSerial, candidateNotAfter, err := g.authority.SignCSR(nodeID, csrPEM)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("%w: %v", errInvalidCredentialCSR, err)
	}
	hash := sha256.Sum256([]byte(csrPEM))
	csrHash := hex.EncodeToString(hash[:])

	type credentialState struct {
		Serial                 *string    `db:"certificate_serial"`
		NotAfter               *time.Time `db:"certificate_not_after"`
		RotationCSRHash        *string    `db:"rotation_csr_sha256"`
		RotationCertificatePEM *string    `db:"rotation_certificate_pem"`
		RevokedAt              *time.Time `db:"revoked_at"`
	}
	certificatePEM, serial, notAfter := candidatePEM, candidateSerial, candidateNotAfter
	err = g.db.Tx(ctx, func(tx *client.Client) error {
		rows, err := client.Raw[credentialState](ctx, tx, `SELECT c.certificate_serial,
			c.certificate_not_after, c.rotation_csr_sha256, c.rotation_certificate_pem, c.revoked_at
			FROM node_credentials c JOIN nodes n ON n.id = c.node_id
			WHERE c.node_id = $1 AND n.status <> 'DISABLED' FOR UPDATE OF c`, nodeID)
		if err != nil {
			return err
		}
		if len(rows) != 1 || rows[0].RevokedAt != nil {
			return errors.New("node credential is unavailable or revoked")
		}
		state := rows[0]
		if state.RotationCSRHash != nil && *state.RotationCSRHash == csrHash {
			if state.Serial == nil || state.NotAfter == nil || state.RotationCertificatePEM == nil {
				return errors.New("stored credential rotation is incomplete")
			}
			certificatePEM, serial, notAfter = *state.RotationCertificatePEM, *state.Serial, *state.NotAfter
			return nil
		}
		if state.Serial == nil || *state.Serial != connectionSerial {
			return errors.New("credential rotation was superseded by another session")
		}
		affected, err := tx.RawExec(ctx, `UPDATE node_credentials SET
			previous_certificate_serial = certificate_serial,
			previous_certificate_valid_until = NOW() + INTERVAL '24 hours',
			certificate_serial = $2, certificate_not_after = $3,
			rotation_csr_sha256 = $4, rotation_certificate_pem = $5
			WHERE node_id = $1 AND certificate_serial = $6 AND revoked_at IS NULL`,
			nodeID, candidateSerial, candidateNotAfter, csrHash, candidatePEM, connectionSerial)
		if err != nil {
			return err
		}
		if affected != 1 {
			return errors.New("credential rotation lost its database lease")
		}
		return nil
	})
	if err != nil {
		return "", "", time.Time{}, err
	}
	return certificatePEM, serial, notAfter, nil
}

func (g *Gateway) Revoke(ctx context.Context, nodeID string) error {
	if err := g.db.Tx(ctx, func(tx *client.Client) error {
		affected, err := tx.RawExec(ctx, `UPDATE node_credentials SET revoked_at = NOW() WHERE node_id = $1`, nodeID)
		if err != nil {
			return err
		}
		if affected != 1 {
			return errors.New("node credential is not registered")
		}
		_, err = tx.RawExec(ctx, `UPDATE agent_tasks SET status = 'CANCELLED',
			cancel_requested_at=NOW(), error='node credential was revoked', lease_owner=NULL,
			lease_until=NULL, heartbeat_at=NULL, updated_at=NOW()
			WHERE node_id = $1 AND status IN ('PENDING', 'RUNNING')`, nodeID)
		return err
	}); err != nil {
		return err
	}
	g.Disconnect(ctx, nodeID)
	return nil
}

func (g *Gateway) Disconnect(ctx context.Context, nodeID string) {
	g.signal(ctx, "disconnect", nodeID)
}

func (g *Gateway) disconnectLocal(nodeID string) {
	g.mu.Lock()
	if active := g.sessions[nodeID]; active != nil {
		active.cancel()
	}
	g.mu.Unlock()
}

func (g *Gateway) releaseLeases(ctx context.Context, nodeID, owner string) {
	_, _ = g.db.RawExec(ctx, `UPDATE agent_tasks SET status = 'PENDING', lease_owner = NULL,
		lease_until = NULL, heartbeat_at=NULL, next_attempt_at=NOW(), updated_at = NOW()
		WHERE node_id = $1 AND lease_owner = $2 AND status = 'RUNNING'
		AND cancel_requested_at IS NULL`, nodeID, owner)
}

func (g *Gateway) register(nodeID string, current *session) {
	g.mu.Lock()
	if previous := g.sessions[nodeID]; previous != nil {
		previous.cancel()
	}
	g.sessions[nodeID] = current
	g.mu.Unlock()
}

func (g *Gateway) unregister(nodeID string, current *session) {
	g.mu.Lock()
	if g.sessions[nodeID] == current {
		delete(g.sessions, nodeID)
	}
	g.mu.Unlock()
}

func (g *Gateway) wakeLocal(nodeID string) {
	g.mu.Lock()
	active := g.sessions[nodeID]
	g.mu.Unlock()
	if active != nil {
		select {
		case active.wake <- struct{}{}:
		default:
		}
	}
}

func (g *Gateway) signal(ctx context.Context, kind, nodeID string) {
	g.applyEvent(gatewayEvent{Kind: kind, NodeID: nodeID})
	if g.db == nil {
		return
	}
	payload, err := json.Marshal(gatewayEvent{Source: g.instanceID, Kind: kind, NodeID: nodeID})
	if err != nil {
		return
	}
	notifyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if _, err := g.db.RawExec(notifyCtx, `SELECT pg_notify($1, $2)`, gatewayEventTopic, string(payload)); err != nil {
		slog.Warn("publish gateway event", "kind", kind, "node_id", nodeID, "error", err)
	}
}

func (g *Gateway) applyEvent(event gatewayEvent) {
	switch event.Kind {
	case "wake":
		g.wakeLocal(event.NodeID)
	case "disconnect":
		g.disconnectLocal(event.NodeID)
	}
}

func (g *Gateway) listenForEvents(ctx context.Context) {
	if g.sqlDB == nil {
		return
	}
	for ctx.Err() == nil {
		if err := g.listenForEventsOnce(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("listen for gateway events", "error", err)
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (g *Gateway) listenForEventsOnce(ctx context.Context) error {
	connection, err := g.sqlDB.Conn(ctx)
	if err != nil {
		return err
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, `LISTEN goveto_edge_gateway_events`); err != nil {
		return err
	}
	for ctx.Err() == nil {
		var payload string
		err := connection.Raw(func(raw any) error {
			stdlibConnection, ok := raw.(*stdlib.Conn)
			if !ok {
				return fmt.Errorf("gateway event listener requires pgx stdlib, got %T", raw)
			}
			notification, waitErr := stdlibConnection.Conn().WaitForNotification(ctx)
			if waitErr == nil {
				payload = notification.Payload
			}
			return waitErr
		})
		if err != nil {
			return err
		}
		var event gatewayEvent
		if json.Unmarshal([]byte(payload), &event) != nil || event.Source == g.instanceID || event.NodeID == "" {
			continue
		}
		g.applyEvent(event)
	}
	return ctx.Err()
}

func (g *Gateway) cleanupTasks(ctx context.Context) {
	affected, err := g.db.RawExec(ctx, `DELETE FROM agent_tasks
		WHERE status IN ('SUCCEEDED', 'FAILED', 'DEAD_LETTER', 'CANCELLED')
		AND updated_at < NOW() - ($1 * INTERVAL '1 second')`, completedTaskTTL.Seconds())
	if err != nil {
		slog.Warn("cleanup completed agent tasks", "error", err)
	} else if affected > 0 {
		slog.Info("cleaned up completed agent tasks", "count", affected)
	}
}

func (g *Gateway) notifyStatusChange(ctx context.Context, clusterID string) {
	if g.onStatusChange != nil {
		g.onStatusChange(ctx, clusterID)
	}
}

func resetTimer(timer *time.Timer, duration time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(duration)
}

func nullableJSON(value json.RawMessage) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
