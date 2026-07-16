package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"reflect"
	"strings"
	"time"

	"goveto-edge/internal/edgecontrol"
	"goveto-edge/internal/edgeprotocol"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

type Lifecycle struct {
	db             *client.Client
	offlineAfter   time.Duration
	http           *http.Client
	cipher         *CredentialCipher
	onStatusChange func(context.Context, string)
	healthFailures map[string]string
}

type healthTarget struct {
	NodeID    string `db:"node_id"`
	ClusterID string `db:"cluster_id"`
	Address   string `db:"address"`
	Status    string `db:"status"`
}

type healthResponse struct {
	NodeID      string                       `json:"node_id"`
	CacheConfig edgeprotocol.NodeCacheConfig `json:"cache_config"`
}

func NewLifecycle(
	db *client.Client,
	cipher *CredentialCipher,
	offlineAfter time.Duration,
	onStatusChange ...func(context.Context, string),
) *Lifecycle {
	lifecycle := &Lifecycle{
		db:             db,
		cipher:         cipher,
		offlineAfter:   offlineAfter,
		http:           &http.Client{Timeout: 5 * time.Second},
		healthFailures: map[string]string{},
	}
	if len(onStatusChange) > 0 {
		lifecycle.onStatusChange = onStatusChange[0]
	}
	return lifecycle
}
func (l *Lifecycle) Run(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		l.poll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (l *Lifecycle) poll(ctx context.Context) {
	targets, err := client.Raw[healthTarget](ctx, l.db, `SELECT n.id AS node_id, n.cluster_id, n.status, a.address
		FROM nodes n
		JOIN node_addresses a ON a.node_id = n.id
		WHERE n.status NOT IN ('PENDING', 'INSTALLING', 'INSTALL_FAILED', 'DISABLED')
		ORDER BY n.id, a.created_at`)
	if err != nil {
		slog.Error("query node health targets", "error", err)
		return
	}

	for start := 0; start < len(targets); {
		end := start + 1
		for end < len(targets) && targets[end].NodeID == targets[start].NodeID {
			end++
		}

		var active *healthTarget
		var health *healthResponse
		failures := make([]string, 0, end-start)
		for index := start; index < end; index++ {
			candidate := targets[index]
			result, checkErr := l.checkHealth(ctx, candidate)
			if checkErr != nil {
				failures = append(failures, candidate.Address+": "+checkErr.Error())
				continue
			}
			active = &candidate
			health = result
			break
		}
		if active == nil {
			l.logHealthFailure(targets[start], strings.Join(failures, "; "))
			start = end
			continue
		}
		if _, failed := l.healthFailures[active.NodeID]; failed {
			slog.Info(
				"node health check recovered",
				"node_id", active.NodeID,
				"address", active.Address,
			)
			delete(l.healthFailures, active.NodeID)
		}

		_, _ = l.db.RawExec(
			ctx,
			`UPDATE nodes SET status = 'ONLINE', install_error = NULL, heartbeat_at = NOW(), updated_at = NOW() WHERE id = $1`,
			active.NodeID,
		)
		if active.Status != "ONLINE" {
			slog.Info(
				"node is online",
				"node_id", active.NodeID,
				"address", active.Address,
			)
			l.notifyStatusChange(ctx, active.ClusterID)
		}
		if err := l.reconcileCacheConfig(ctx, *active, health.CacheConfig); err != nil {
			slog.Warn(
				"reconcile node cache config",
				"node_id", active.NodeID,
				"address", active.Address,
				"error", err,
			)
		}
		start = end
	}
	l.markOffline(ctx)
}

// InitializeInstalledNode verifies a manually installed agent, synchronizes
// its node-level configuration, and admits it into the normal health cycle.
func InitializeInstalledNode(
	ctx context.Context,
	db *client.Client,
	cipher *CredentialCipher,
	clusterID, nodeID string,
) (string, error) {
	addresses, err := db.NodeAddress.Query().
		Where(query.NodeAddress.NodeId.Equals(nodeID)).
		OrderBy(query.NodeAddress.CreatedAt.Asc()).
		Do(ctx)
	if err != nil {
		return "", fmt.Errorf("load node addresses: %w", err)
	}
	if len(addresses) == 0 {
		return "", errors.New("node has no address to initialize")
	}

	lifecycle := NewLifecycle(db, cipher, 45*time.Second)
	failures := make([]string, 0, len(addresses))
	for _, address := range addresses {
		target := healthTarget{
			NodeID:    nodeID,
			ClusterID: clusterID,
			Address:   address.Address,
			Status:    string(model.NodeStatusOFFLINE),
		}
		health, healthErr := lifecycle.checkHealth(ctx, target)
		if healthErr != nil {
			failures = append(failures, address.Address+": "+healthErr.Error())
			continue
		}
		if syncErr := lifecycle.reconcileCacheConfig(ctx, target, health.CacheConfig); syncErr != nil {
			return "", fmt.Errorf("synchronize node configuration through %s: %w", address.Address, syncErr)
		}
		if _, updateErr := db.Node.Update().
			Where(query.Node.Id.Equals(nodeID)).
			Set(
				query.Node.Status.Set(model.NodeStatusONLINE),
				query.Node.InstallError.SetNull(),
				query.Node.HeartbeatAt.Set(time.Now()),
			).
			Do(ctx); updateErr != nil {
			return "", fmt.Errorf("complete node initialization: %w", updateErr)
		}
		return address.Address, nil
	}

	return "", fmt.Errorf("agent health check failed: %s", strings.Join(failures, "; "))
}

func (l *Lifecycle) checkHealth(ctx context.Context, target healthTarget) (*healthResponse, error) {
	request, err := newHealthRequest(ctx, target)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	response, err := l.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected HTTP status %s", response.Status)
	}
	var health healthResponse
	if err := json.NewDecoder(response.Body).Decode(&health); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if health.NodeID != target.NodeID {
		return nil, fmt.Errorf("node ID mismatch: got %q", health.NodeID)
	}
	return &health, nil
}

func newHealthRequest(ctx context.Context, target healthTarget) (*http.Request, error) {
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		"http://"+net.JoinHostPort(target.Address, "80")+"/v1/health",
		nil,
	)
	if err != nil {
		return nil, err
	}
	// The Agent API is routed by Caddy using the node UUID as its Host matcher.
	request.Host = target.NodeID
	return request, nil
}

func (l *Lifecycle) logHealthFailure(target healthTarget, reason string) {
	if l.healthFailures[target.NodeID] == reason {
		return
	}
	l.healthFailures[target.NodeID] = reason
	slog.Warn(
		"node health check failed",
		"node_id", target.NodeID,
		"address", target.Address,
		"host", target.NodeID,
		"reason", reason,
	)
}

func (l *Lifecycle) reconcileCacheConfig(ctx context.Context, target healthTarget, current edgeprotocol.NodeCacheConfig) error {
	stored, err := l.db.NodeCacheConfig.FindUnique(ctx, query.NodeCacheConfig.NodeId.Equals(target.NodeID))
	if err != nil {
		return err
	}
	if stored == nil {
		return errors.New("node cache configuration is missing")
	}

	desired := edgeprotocol.NodeCacheConfig{
		CacheDirectory:      stored.CacheDir,
		AutoMaxSize:         stored.AutoMaxSize,
		MaxDiskUsagePercent: stored.MaxDiskUsagePercent,
	}
	if stored.MaxSizeBytes != nil {
		desired.MaxSizeBytes = uint64(*stored.MaxSizeBytes)
	}
	if reflect.DeepEqual(current, desired) {
		return nil
	}

	credential, err := l.db.NodeCredential.FindUnique(ctx, query.NodeCredential.NodeId.Equals(target.NodeID))
	if err != nil {
		return err
	}
	if credential == nil {
		return errors.New("node communication credential is missing")
	}

	key, err := l.cipher.Decrypt(credential.CommunicationKeyEncrypted)
	if err != nil {
		return err
	}

	return edgecontrol.New(
		"http://"+net.JoinHostPort(target.Address, "80"),
		target.NodeID,
		key,
	).PushNodeCacheConfig(ctx, desired)
}
func (l *Lifecycle) markOffline(ctx context.Context) {
	type changedCluster struct {
		ClusterID string `db:"cluster_id"`
	}
	clusters, err := client.Raw[changedCluster](ctx, l.db, `UPDATE nodes SET status = 'OFFLINE', updated_at = NOW()
			WHERE status = 'ONLINE' AND (heartbeat_at IS NULL OR heartbeat_at < NOW() - ($1 * INTERVAL '1 second'))
			RETURNING cluster_id`, l.offlineAfter.Seconds())
	if err != nil {
		return
	}
	notified := map[string]struct{}{}
	for _, cluster := range clusters {
		if _, exists := notified[cluster.ClusterID]; exists {
			continue
		}
		notified[cluster.ClusterID] = struct{}{}
		l.notifyStatusChange(ctx, cluster.ClusterID)
	}
}

func (l *Lifecycle) notifyStatusChange(ctx context.Context, clusterID string) {
	if l.onStatusChange != nil {
		l.onStatusChange(ctx, clusterID)
	}
}
