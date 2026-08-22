package node

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
	"golang.org/x/mod/semver"

	"goveto-edge/internal/buildinfo"
	"goveto-edge/internal/jobqueue"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
	staticassets "goveto-edge/static"
)

const (
	agentUpgradeScanInterval  = 15 * time.Second
	agentUpgradePollInterval  = 2 * time.Second
	agentUpgradeVerifyTimeout = 60 * time.Second
	agentUpgradeWorkerCount   = 4
	agentUpgradeAttemptLimit  = 12 * time.Minute

	agentUpgradeMarker         = "/var/lib/goveto-edge-agent/upgrade-pending"
	agentUpgradeBackup         = "/usr/local/bin/goveto-edge-agent.previous"
	agentUpgradeRollbackScript = "/usr/local/lib/goveto-edge-agent/rollback-upgrade"
	agentUpgradeRollbackUnit   = "/etc/systemd/system/goveto-edge-agent-rollback.service"
	agentUpgradeRollbackTimer  = "/etc/systemd/system/goveto-edge-agent-rollback.timer"
	agentUpgradeDisarmedOutput = "goveto-edge-agent-upgrade-disarmed"
)

const agentUpgradeRollbackScriptBody = `#!/bin/sh
set -eu
marker=/var/lib/goveto-edge-agent/upgrade-pending
backup=/usr/local/bin/goveto-edge-agent.previous
if [ ! -e "$marker" ]; then
	exit 0
fi
if [ ! -x "$backup" ]; then
	echo 'agent rollback backup is missing' >&2
	exit 1
fi
install -m 0755 "$backup" /usr/local/bin/goveto-edge-agent
systemctl restart goveto-edge-agent
systemctl is-active --quiet goveto-edge-agent
rm -f "$marker"
`

const agentUpgradeRollbackServiceUnit = `[Unit]
Description=Rollback an unverified Goveto Edge Agent upgrade
After=network-online.target

[Service]
Type=oneshot
ExecStart=/usr/local/lib/goveto-edge-agent/rollback-upgrade
`

const agentUpgradeRollbackTimerUnit = `[Unit]
Description=Rollback an unverified Goveto Edge Agent upgrade after 90 seconds

[Timer]
OnActiveSec=90s
AccuracySec=1s
Unit=goveto-edge-agent-rollback.service

[Install]
WantedBy=timers.target
`

type AgentUpgradeQueue struct {
	db   *client.Client
	jobs *jobqueue.Manager
}

type AgentUpgradeEnqueueResult struct {
	Job    *model.AgentUpgradeJob
	Reused bool
}

func NewAgentUpgradeQueue(db *client.Client) *AgentUpgradeQueue {
	return &AgentUpgradeQueue{db: db, jobs: jobqueue.New(db)}
}

func (q *AgentUpgradeQueue) Enqueue(ctx context.Context, nodeID, targetVersion, trigger string) (AgentUpgradeEnqueueResult, error) {
	if !semver.IsValid(targetVersion) {
		return AgentUpgradeEnqueueResult{}, fmt.Errorf("agent upgrade target %q is not a release version", targetVersion)
	}
	if trigger != "automatic" && trigger != "manual" {
		return AgentUpgradeEnqueueResult{}, errors.New("invalid agent upgrade trigger")
	}
	payload, err := json.Marshal(map[string]string{"trigger": trigger})
	if err != nil {
		return AgentUpgradeEnqueueResult{}, err
	}
	result := AgentUpgradeEnqueueResult{}
	err = q.db.Tx(ctx, func(tx *client.Client) error {
		if _, lockErr := tx.RawExec(ctx,
			"SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", "agent-upgrade:node:"+nodeID,
		); lockErr != nil {
			return lockErr
		}
		latest, findErr := tx.AgentUpgradeJob.Query().
			Where(query.AgentUpgradeJob.NodeId.Equals(nodeID)).
			OrderBy(query.AgentUpgradeJob.CreatedAt.Desc()).
			Take(1).
			Do(ctx)
		if findErr != nil {
			return findErr
		}
		if len(latest) == 1 {
			previous := &latest[0]
			if shouldReuseAgentUpgrade(previous, targetVersion, trigger) {
				result = AgentUpgradeEnqueueResult{Job: previous, Reused: true}
				return nil
			}
		}
		created, createErr := tx.AgentUpgradeJob.Create().Set(
			query.AgentUpgradeJob.Id.Set(uuid.NewString()),
			query.AgentUpgradeJob.NodeId.Set(nodeID),
			query.AgentUpgradeJob.TargetVersion.Set(targetVersion),
			query.AgentUpgradeJob.Payload.Set(payload),
			query.AgentUpgradeJob.MaxAttempts.Set(5),
		).Do(ctx)
		if createErr != nil {
			return createErr
		}
		result.Job = created
		return nil
	})
	return result, err
}

func shouldReuseAgentUpgrade(previous *model.AgentUpgradeJob, targetVersion, trigger string) bool {
	if previous == nil || previous.TargetVersion != targetVersion {
		return false
	}
	active := previous.Status == model.JobStatusPENDING || previous.Status == model.JobStatusRUNNING
	terminalFailure := previous.Status == model.JobStatusFAILED || previous.Status == model.JobStatusDEAD_LETTER
	return active || (trigger == "automatic" && terminalFailure)
}

func (q *AgentUpgradeQueue) CancelPending(ctx context.Context) error {
	// Running replacements must finish verification or rollback. Disabling the
	// setting only prevents work that has not started yet.
	_, err := q.db.RawExec(ctx, `UPDATE agent_upgrade_jobs SET status='CANCELLED',
		cancel_requested_at=NOW(), error=COALESCE(error, 'automatic agent upgrades disabled'),
		updated_at=NOW() WHERE status='PENDING' AND payload->>'trigger'='automatic'`)
	return err
}

func (q *AgentUpgradeQueue) RunOne(
	ctx context.Context,
	handler func(context.Context, jobqueue.Lease) jobqueue.Outcome,
) (bool, error) {
	return q.jobs.RunOne(ctx, jobqueue.AgentUpgrade, 0, handler)
}

type AgentUpgradeService struct {
	db              *client.Client
	queue           *AgentUpgradeQueue
	cipher          *CredentialCipher
	settings        AgentUpgradeSettings
	target          string
	terminalNotices sync.Map
}

type AgentUpgradeSettings interface {
	AgentAutoUpgradeEnabled(context.Context) (bool, error)
}

func NewAgentUpgradeService(
	db *client.Client,
	queue *AgentUpgradeQueue,
	cipher *CredentialCipher,
	settingStore AgentUpgradeSettings,
	targetVersion string,
) *AgentUpgradeService {
	return &AgentUpgradeService{db: db, queue: queue, cipher: cipher, settings: settingStore, target: targetVersion}
}

// ValidateAgentArtifactVersion prevents a release control plane from serving a
// different embedded agent version. Development builds intentionally disable
// automatic upgrades and can run without generated release artifacts.
func ValidateAgentArtifactVersion(controlVersion string) error {
	agentVersion, err := staticassets.AgentVersion()
	if err != nil {
		if !semver.IsValid(controlVersion) && errors.Is(err, staticassets.ErrAgentArtifactsUnavailable) {
			return nil
		}
		return fmt.Errorf("load embedded agent artifact (run script/build_agent.sh before a release build): %w", err)
	}
	if semver.IsValid(controlVersion) && agentVersion != controlVersion {
		return fmt.Errorf("embedded agent version %q does not match control plane version %q", agentVersion, controlVersion)
	}
	return nil
}

func (s *AgentUpgradeService) Run(ctx context.Context) {
	if !buildinfo.IsRelease() || s.target != buildinfo.Current() {
		slog.Info("automatic agent upgrades disabled for development build", "version", s.target)
		return
	}
	var workers sync.WaitGroup
	for range agentUpgradeWorkerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			s.workerLoop(ctx)
		}()
	}

	s.reconcile(ctx)
	ticker := time.NewTicker(agentUpgradeScanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			workers.Wait()
			return
		case <-ticker.C:
			s.reconcile(ctx)
		}
	}
}

func (s *AgentUpgradeService) reconcile(ctx context.Context) {
	enabled, err := s.settings.AgentAutoUpgradeEnabled(ctx)
	if err != nil {
		slog.Warn("read automatic agent upgrade setting", "error", err)
		return
	}
	if !enabled {
		if err = s.queue.CancelPending(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("cancel pending agent upgrades", "error", err)
		}
		return
	}
	nodes, err := s.db.Node.Query().Where(query.Node.Status.Equals(model.NodeStatusONLINE)).Do(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("scan online agents for upgrades", "error", err)
		}
		return
	}
	for index := range nodes {
		current := ""
		if nodes[index].Version != nil {
			current = *nodes[index].Version
		}
		if !buildinfo.NeedsUpgrade(current, s.target) {
			continue
		}
		result, enqueueErr := s.queue.Enqueue(ctx, nodes[index].Id, s.target, "automatic")
		if enqueueErr != nil {
			if ctx.Err() == nil {
				slog.Warn("enqueue automatic agent upgrade", "node_id", nodes[index].Id, "error", enqueueErr)
			}
			continue
		}
		if result.Reused && result.Job != nil &&
			(result.Job.Status == model.JobStatusFAILED || result.Job.Status == model.JobStatusDEAD_LETTER) {
			if _, loaded := s.terminalNotices.LoadOrStore(result.Job.Id, struct{}{}); !loaded {
				slog.Info("automatic agent upgrade requires manual retry", "node_id", nodes[index].Id,
					"job_id", result.Job.Id, "target_version", result.Job.TargetVersion, "status", result.Job.Status)
			}
		}
	}
}

func (s *AgentUpgradeService) workerLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		s.runOne(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *AgentUpgradeService) runOne(ctx context.Context) {
	_, err := s.queue.RunOne(ctx, func(runCtx context.Context, lease jobqueue.Lease) jobqueue.Outcome {
		attemptCtx, cancel := context.WithTimeout(runCtx, agentUpgradeAttemptLimit)
		defer cancel()
		return s.execute(attemptCtx, lease)
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		slog.Warn("agent upgrade worker", "error", err)
	}
}

func (s *AgentUpgradeService) execute(ctx context.Context, lease jobqueue.Lease) jobqueue.Outcome {
	job, err := s.db.AgentUpgradeJob.FindUnique(ctx, query.AgentUpgradeJob.Id.Equals(lease.ID))
	if err != nil || job == nil {
		if err == nil {
			err = errors.New("agent upgrade job not found")
		}
		return agentUpgradeFailure(lease, err)
	}
	node, err := s.db.Node.FindUnique(ctx, query.Node.Id.Equals(job.NodeId))
	if err != nil || node == nil {
		if err == nil {
			err = permanentInstallError(errors.New("agent upgrade node not found"))
		}
		return agentUpgradeFailure(lease, err)
	}
	current := ""
	if node.Version != nil {
		current = *node.Version
	}
	if !buildinfo.NeedsUpgrade(current, job.TargetVersion) {
		return jobqueue.Outcome{Result: map[string]any{
			"node_id": node.Id, "version": current, "target_version": job.TargetVersion, "skipped": true,
		}}
	}
	if node.Status != model.NodeStatusONLINE {
		return agentUpgradeFailure(lease, errors.New("node is not online"))
	}
	if err = validateAgentUpgradeSSH(node); err != nil {
		return agentUpgradeFailure(lease, err)
	}

	lock, err := acquireClusterUpgradeLock(ctx, s.db.DB(), node.ClusterId)
	if err != nil {
		return agentUpgradeFailure(lease, err)
	}
	if lock == nil {
		return jobqueue.Outcome{RequeueAfter: 15 * time.Second}
	}
	defer lock.Close(ctx)

	// Re-read after acquiring the cluster lock: another control-plane replica
	// may have upgraded this node while this job was waiting.
	node, err = s.db.Node.FindUnique(ctx, query.Node.Id.Equals(job.NodeId))
	if err != nil || node == nil {
		if err == nil {
			err = permanentInstallError(errors.New("agent upgrade node not found"))
		}
		return agentUpgradeFailure(lease, err)
	}
	current = ""
	if node.Version != nil {
		current = *node.Version
	}
	if !buildinfo.NeedsUpgrade(current, job.TargetVersion) {
		return jobqueue.Outcome{Result: map[string]any{
			"node_id": node.Id, "version": current, "target_version": job.TargetVersion, "skipped": true,
		}}
	}
	if node.Status != model.NodeStatusONLINE {
		return agentUpgradeFailure(lease, errors.New("node is not online"))
	}
	if err = validateAgentUpgradeSSH(node); err != nil {
		return agentUpgradeFailure(lease, err)
	}

	_, sshInput, err := ResolveSSHInstallInput(ctx, s.db, s.cipher, node.ClusterId, SSHInstallReference{
		EntryIP: *node.SshHost, Port: uint16(*node.SshPort), CredentialID: *node.SshCredentialId,
	})
	if err != nil {
		return agentUpgradeFailure(lease, err)
	}
	verifier, err := PinnedHostKeyVerifier(ctx, s.db, node.Id)
	if err != nil {
		return agentUpgradeFailure(lease, err)
	}
	connection, err := connectSSH(ctx, sshInput, verifier)
	if err != nil {
		return agentUpgradeFailure(lease, err)
	}
	defer connection.Close()

	arch, err := remoteArchitecture(ctx, connection)
	if err != nil {
		return agentUpgradeFailure(lease, err)
	}
	artifact, err := staticassets.Agent(arch)
	if err != nil {
		return agentUpgradeFailure(lease, permanentInstallError(err))
	}
	if artifact.Version != job.TargetVersion {
		return agentUpgradeFailure(lease, permanentInstallError(fmt.Errorf(
			"embedded agent version %q does not match job target %q", artifact.Version, job.TargetVersion,
		)))
	}

	err = s.replaceAndVerify(ctx, connection, sshInput.User, node.Id, job.Id, job.TargetVersion, artifact)
	if err != nil {
		slog.Error("agent upgrade failed", "node_id", node.Id, "target_version", job.TargetVersion, "error", err)
		return agentUpgradeFailure(lease, err)
	}
	slog.Info("agent upgrade completed", "node_id", node.Id, "from_version", current, "version", job.TargetVersion)
	return jobqueue.Outcome{Result: map[string]any{
		"node_id": node.Id, "previous_version": current, "version": job.TargetVersion,
	}}
}

func validateAgentUpgradeSSH(node *model.Node) error {
	if node.SshCredentialId == nil || node.SshHost == nil || node.SshPort == nil {
		return permanentInstallError(errors.New("node SSH upgrade configuration is missing"))
	}
	if *node.SshPort < 1 || *node.SshPort > 65535 {
		return permanentInstallError(errors.New("node SSH port is invalid"))
	}
	return nil
}

func agentUpgradeFailure(lease jobqueue.Lease, err error) jobqueue.Outcome {
	retryable := !errors.Is(err, errPermanentInstallConfiguration)
	outcome := jobqueue.Outcome{Err: err, Retryable: retryable}
	if retryable && lease.Attempt < lease.MaxAttempts {
		shift := lease.Attempt - 1
		if shift < 0 {
			shift = 0
		}
		if shift > 3 {
			shift = 3
		}
		outcome.RetryAfter = time.Minute << shift
	}
	return outcome
}

type clusterUpgradeLock struct {
	conn      *sql.Conn
	clusterID string
}

func acquireClusterUpgradeLock(ctx context.Context, db *sql.DB, clusterID string) (*clusterUpgradeLock, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	var locked bool
	err = conn.QueryRowContext(ctx,
		"SELECT pg_try_advisory_lock(hashtextextended($1, 0))", "agent-upgrade:cluster:"+clusterID,
	).Scan(&locked)
	if err != nil || !locked {
		_ = conn.Close()
		return nil, err
	}
	return &clusterUpgradeLock{conn: conn, clusterID: clusterID}, nil
}

func (l *clusterUpgradeLock) Close(ctx context.Context) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var unlocked bool
	err := l.conn.QueryRowContext(cleanupCtx,
		"SELECT pg_advisory_unlock(hashtextextended($1, 0))", "agent-upgrade:cluster:"+l.clusterID,
	).Scan(&unlocked)
	if err != nil || !unlocked {
		_ = l.conn.Raw(func(any) error { return driver.ErrBadConn })
	}
	_ = l.conn.Close()
}

func (s *AgentUpgradeService) replaceAndVerify(
	ctx context.Context,
	connection *ssh.Client,
	sshUser, nodeID, jobID, targetVersion string,
	artifact staticassets.AgentArtifact,
) error {
	candidate := "/tmp/goveto-edge-agent-upgrade-" + jobID
	rollbackUpload := "/tmp/goveto-edge-agent-rollback-" + jobID
	serviceUpload := rollbackUpload + ".service"
	timerUpload := rollbackUpload + ".timer"
	privileged := privilegedCommandPrefix(sshUser)
	defer func() {
		_, _ = runRemoteCommand(context.WithoutCancel(ctx), connection, 30*time.Second,
			"sh -c "+shellQuote(agentUpgradeCleanupScript(privileged, candidate, rollbackUpload, serviceUpload, timerUpload)))
	}()
	for _, upload := range []struct {
		path    string
		content []byte
		mode    uint32
		timeout time.Duration
	}{
		{candidate, artifact.Binary, 0755, 5 * time.Minute},
		{rollbackUpload, []byte(agentUpgradeRollbackScriptBody), 0755, 30 * time.Second},
		{serviceUpload, []byte(agentUpgradeRollbackServiceUnit), 0644, 30 * time.Second},
		{timerUpload, []byte(agentUpgradeRollbackTimerUnit), 0644, 30 * time.Second},
	} {
		if err := uploadSCP(ctx, connection, upload.path, upload.content, upload.mode, upload.timeout); err != nil {
			return fmt.Errorf("upload agent upgrade artifact: %w", err)
		}
	}

	applyScript := agentUpgradeApplyScript(
		privileged, candidate, rollbackUpload, serviceUpload, timerUpload, targetVersion, artifact.SHA256,
	)
	startedAt, err := s.upgradeVerificationStart(ctx)
	if err != nil {
		return fmt.Errorf("read database clock before agent replacement: %w", err)
	}
	output, applyErr := runRemoteCommand(ctx, connection, 90*time.Second, "sh -c "+shellQuote(applyScript))
	if applyErr != nil {
		applyErr = fmt.Errorf("replace and restart agent: %w%s", applyErr, commandOutputSuffix(output))
	}
	if applyErr == nil {
		applyErr = s.waitForAgentVersion(ctx, nodeID, targetVersion, startedAt)
	}
	if applyErr == nil {
		output, applyErr = runRemoteCommand(ctx, connection, 30*time.Second,
			"sh -c "+shellQuote(agentUpgradeDisarmScript(privileged)))
		if applyErr != nil && bytes.Contains(output, []byte(agentUpgradeDisarmedOutput)) {
			applyErr = nil
		}
		if applyErr != nil {
			applyErr = fmt.Errorf("disarm agent rollback watchdog: %w%s", applyErr, commandOutputSuffix(output))
		}
	}
	if applyErr == nil {
		return nil
	}
	rollbackOutput, rollbackErr := runRemoteCommand(
		context.WithoutCancel(ctx), connection, 45*time.Second,
		"sh -c "+shellQuote(agentUpgradeProactiveRollbackScript(privileged)),
	)
	if rollbackErr != nil {
		return fmt.Errorf("%w; proactive rollback failed (remote watchdog remains armed): %v%s",
			applyErr, rollbackErr, commandOutputSuffix(rollbackOutput))
	}
	return fmt.Errorf("%w; previous agent binary restored", applyErr)
}

func (s *AgentUpgradeService) upgradeVerificationStart(ctx context.Context) (time.Time, error) {
	type databaseClock struct {
		Now time.Time `db:"now"`
	}
	rows, err := client.Raw[databaseClock](ctx, s.db, "SELECT clock_timestamp() - INTERVAL '2 seconds' AS now")
	if err != nil {
		return time.Time{}, err
	}
	if len(rows) != 1 {
		return time.Time{}, errors.New("database clock query returned no row")
	}
	return rows[0].Now, nil
}

func (s *AgentUpgradeService) waitForAgentVersion(
	ctx context.Context,
	nodeID, targetVersion string,
	startedAt time.Time,
) error {
	verifyCtx, cancel := context.WithTimeout(ctx, agentUpgradeVerifyTimeout)
	defer cancel()
	ticker := time.NewTicker(agentUpgradePollInterval)
	defer ticker.Stop()
	for {
		type state struct {
			Ready bool `db:"ready"`
		}
		rows, err := client.Raw[state](verifyCtx, s.db, `SELECT EXISTS (
			SELECT 1 FROM nodes WHERE id=$1 AND status='ONLINE' AND version=$2
			AND heartbeat_at >= $3) AS ready`, nodeID, targetVersion, startedAt)
		if err == nil && len(rows) == 1 && rows[0].Ready {
			return nil
		}
		if err != nil && verifyCtx.Err() == nil {
			slog.Warn("verify upgraded agent hello", "node_id", nodeID, "error", err)
		}
		select {
		case <-verifyCtx.Done():
			return fmt.Errorf("new agent did not report version %s before rollback deadline: %w", targetVersion, verifyCtx.Err())
		case <-ticker.C:
		}
	}
}

func agentUpgradeApplyScript(
	privileged, candidate, rollbackUpload, serviceUpload, timerUpload, targetVersion, sha256 string,
) string {
	expectedOutput := "edge-agent " + targetVersion
	return fmt.Sprintf(`set -eu
candidate=%s
actual_hash=$(sha256sum "$candidate" | awk '{print $1}')
test "$actual_hash" = %s
actual_version=$("$candidate" version)
test "$actual_version" = %s
test -x /usr/local/bin/goveto-edge-agent
if %[4]stest -e %[5]s; then
	echo 'a previous agent upgrade is still awaiting rollback' >&2
	exit 75
fi
%[4]ssystemctl stop goveto-edge-agent-rollback.timer >/dev/null 2>&1 || true
%[4]sinstall -d -m 0755 /var/lib/goveto-edge-agent /usr/local/lib/goveto-edge-agent
%[4]sinstall -m 0755 /usr/local/bin/goveto-edge-agent %[6]s
%[4]sinstall -m 0755 %[7]s %[8]s
%[4]sinstall -m 0644 %[9]s %[10]s
%[4]sinstall -m 0644 %[11]s %[12]s
%[4]ssystemctl daemon-reload
%[4]stouch %[5]s
%[4]ssystemctl start goveto-edge-agent-rollback.timer
%[4]sinstall -m 0755 "$candidate" /usr/local/bin/goveto-edge-agent
%[4]ssystemctl restart goveto-edge-agent
%[4]ssystemctl is-active --quiet goveto-edge-agent
`, shellQuote(candidate), shellQuote(sha256), shellQuote(expectedOutput), privileged,
		shellQuote(agentUpgradeMarker), shellQuote(agentUpgradeBackup), shellQuote(rollbackUpload),
		shellQuote(agentUpgradeRollbackScript), shellQuote(serviceUpload), shellQuote(agentUpgradeRollbackUnit),
		shellQuote(timerUpload), shellQuote(agentUpgradeRollbackTimer))
}

func agentUpgradeDisarmScript(privileged string) string {
	return fmt.Sprintf(`set -eu
%[1]srm -f %[2]s
echo %[7]s
%[1]ssystemctl stop goveto-edge-agent-rollback.timer >/dev/null 2>&1 || true
%[1]srm -f %[3]s %[4]s %[5]s %[6]s
%[1]ssystemctl daemon-reload >/dev/null 2>&1 || true
`, privileged, shellQuote(agentUpgradeMarker), shellQuote(agentUpgradeBackup),
		shellQuote(agentUpgradeRollbackScript), shellQuote(agentUpgradeRollbackUnit), shellQuote(agentUpgradeRollbackTimer),
		shellQuote(agentUpgradeDisarmedOutput))
}

func agentUpgradeCleanupScript(privileged string, paths ...string) string {
	quoted := make([]string, 0, len(paths))
	for _, path := range paths {
		quoted = append(quoted, shellQuote(path))
	}
	return privileged + "rm -f " + strings.Join(quoted, " ")
}

func agentUpgradeProactiveRollbackScript(privileged string) string {
	return fmt.Sprintf(`set -eu
%[1]ssystemctl stop goveto-edge-agent-rollback.timer >/dev/null 2>&1 || true
if %[1]stest -e %[2]s; then
	%[1]s%[3]s
fi
`, privileged, shellQuote(agentUpgradeMarker), agentUpgradeRollbackScript)
}
