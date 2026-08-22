package node

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	"goveto-edge/internal/buildinfo"
	"goveto-edge/internal/jobqueue"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
)

func TestValidateAgentArtifactVersionRejectsReleaseMismatch(t *testing.T) {
	original := buildinfo.Version
	t.Cleanup(func() { buildinfo.Version = original })
	buildinfo.Version = "dev"
	if err := ValidateAgentArtifactVersion("dev"); err != nil {
		t.Fatalf("development artifact validation failed: %v", err)
	}
	buildinfo.Version = "v9.9.9"
	if err := ValidateAgentArtifactVersion("v9.9.9"); err == nil {
		t.Fatal("release control plane accepted the embedded development agent")
	}
}

func TestAgentUpgradeFailureRetrySchedule(t *testing.T) {
	for _, test := range []struct {
		attempt int
		want    time.Duration
	}{
		{1, time.Minute},
		{2, 2 * time.Minute},
		{3, 4 * time.Minute},
		{4, 8 * time.Minute},
	} {
		outcome := agentUpgradeFailure(jobqueue.Lease{Attempt: test.attempt, MaxAttempts: 5}, errors.New("temporary"))
		if !outcome.Retryable || outcome.RetryAfter != test.want {
			t.Fatalf("attempt %d outcome = %+v, want retry after %s", test.attempt, outcome, test.want)
		}
	}
	last := agentUpgradeFailure(jobqueue.Lease{Attempt: 5, MaxAttempts: 5}, errors.New("temporary"))
	if !last.Retryable || last.RetryAfter != 0 {
		t.Fatalf("last attempt outcome = %+v", last)
	}
}

func TestShouldReuseAgentUpgrade(t *testing.T) {
	for _, test := range []struct {
		status    model.JobStatus
		previous  string
		target    string
		trigger   string
		wantReuse bool
	}{
		{model.JobStatusPENDING, "v1.2.3", "v1.2.3", "automatic", true},
		{model.JobStatusRUNNING, "v1.2.3", "v1.2.3", "manual", true},
		{model.JobStatusFAILED, "v1.2.3", "v1.2.3", "automatic", true},
		{model.JobStatusDEAD_LETTER, "v1.2.3", "v1.2.3", "manual", false},
		{model.JobStatusCANCELLED, "v1.2.3", "v1.2.3", "automatic", false},
		{model.JobStatusPENDING, "v1.2.3", "v1.3.0", "automatic", false},
	} {
		job := &model.AgentUpgradeJob{Status: test.status, TargetVersion: test.previous}
		if got := shouldReuseAgentUpgrade(job, test.target, test.trigger); got != test.wantReuse {
			t.Fatalf("reuse status=%s previous=%s target=%s trigger=%s = %t; want %t",
				test.status, test.previous, test.target, test.trigger, got, test.wantReuse)
		}
	}
}

func TestCancelPendingOnlyTargetsQueuedAutomaticUpgrades(t *testing.T) {
	var executed string
	database := sql.OpenDB(cancelCaptureConnector{executed: &executed})
	t.Cleanup(func() { _ = database.Close() })
	queue := NewAgentUpgradeQueue(client.New(database))
	if err := queue.CancelPending(context.Background()); err != nil {
		t.Fatalf("CancelPending() error: %v", err)
	}
	normalized := strings.Join(strings.Fields(executed), " ")
	for _, required := range []string{"status='PENDING'", "payload->>'trigger'='automatic'"} {
		if !strings.Contains(normalized, required) {
			t.Fatalf("CancelPending query is missing %q: %s", required, normalized)
		}
	}
	if strings.Contains(normalized, "status='RUNNING'") {
		t.Fatalf("CancelPending query targets running upgrades: %s", normalized)
	}
}

type cancelCaptureConnector struct{ executed *string }

func (c cancelCaptureConnector) Connect(context.Context) (driver.Conn, error) {
	return cancelCaptureConn{executed: c.executed}, nil
}

func (cancelCaptureConnector) Driver() driver.Driver { return cancelCaptureDriver{} }

type cancelCaptureDriver struct{}

func (cancelCaptureDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("cancel capture driver requires Connector")
}

type cancelCaptureConn struct{ executed *string }

func (c cancelCaptureConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	*c.executed = query
	return driver.RowsAffected(1), nil
}

func (cancelCaptureConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not supported")
}

func (cancelCaptureConn) Close() error { return nil }
func (cancelCaptureConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not supported")
}

func TestAgentUpgradePermanentConfigurationFailureDoesNotRetry(t *testing.T) {
	outcome := agentUpgradeFailure(
		jobqueue.Lease{Attempt: 1, MaxAttempts: 5},
		permanentInstallError(errors.New("SSH configuration is missing")),
	)
	if outcome.Retryable || outcome.RetryAfter != 0 {
		t.Fatalf("permanent failure outcome = %+v", outcome)
	}
}

func TestValidateAgentUpgradeSSH(t *testing.T) {
	credentialID := "credential-1"
	host := "192.0.2.10"
	validPort := 22
	if err := validateAgentUpgradeSSH(&model.Node{
		SshCredentialId: &credentialID, SshHost: &host, SshPort: &validPort,
	}); err != nil {
		t.Fatalf("valid SSH configuration rejected: %v", err)
	}
	for _, node := range []*model.Node{
		{},
		{SshCredentialId: &credentialID, SshHost: &host, SshPort: intPointer(0)},
		{SshCredentialId: &credentialID, SshHost: &host, SshPort: intPointer(65536)},
	} {
		if err := validateAgentUpgradeSSH(node); !errors.Is(err, errPermanentInstallConfiguration) {
			t.Fatalf("invalid SSH configuration returned %v", err)
		}
	}
}

func intPointer(value int) *int { return &value }

func TestAgentUpgradeApplyScriptArmsRollbackBeforeReplacement(t *testing.T) {
	script := agentUpgradeApplyScript(
		"sudo ", "/tmp/candidate", "/tmp/rollback", "/tmp/rollback.service",
		"/tmp/rollback.timer", "v1.2.3", strings.Repeat("a", 64),
	)
	for _, required := range []string{
		"edge-agent v1.2.3",
		"sudo test -e '/var/lib/goveto-edge-agent/upgrade-pending'",
		"sudo install -m 0755 /usr/local/bin/goveto-edge-agent '/usr/local/bin/goveto-edge-agent.previous'",
		"sudo systemctl start goveto-edge-agent-rollback.timer",
		"sudo install -m 0755 \"$candidate\" /usr/local/bin/goveto-edge-agent",
		"sudo systemctl restart goveto-edge-agent",
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("upgrade script is missing %q:\n%s", required, script)
		}
	}
	backup := strings.Index(script, "goveto-edge-agent.previous")
	markerCheck := strings.Index(script, "test -e '"+agentUpgradeMarker+"'")
	stopOldTimer := strings.Index(script, "systemctl stop goveto-edge-agent-rollback.timer")
	arm := strings.Index(script, "systemctl start goveto-edge-agent-rollback.timer")
	replace := strings.Index(script, "install -m 0755 \"$candidate\" /usr/local/bin/goveto-edge-agent")
	if markerCheck < 0 || stopOldTimer < markerCheck || backup < stopOldTimer || arm < backup || replace < arm {
		t.Fatalf("rollback ordering is unsafe: marker=%d stop=%d backup=%d arm=%d replace=%d\n%s",
			markerCheck, stopOldTimer, backup, arm, replace, script)
	}
}

func TestAgentUpgradeDisarmRemovesRollbackState(t *testing.T) {
	script := agentUpgradeDisarmScript("sudo ")
	for _, required := range []string{
		"systemctl stop goveto-edge-agent-rollback.timer",
		agentUpgradeDisarmedOutput,
		agentUpgradeMarker,
		agentUpgradeBackup,
		agentUpgradeRollbackScript,
		agentUpgradeRollbackUnit,
		agentUpgradeRollbackTimer,
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("disarm script is missing %q: %s", required, script)
		}
	}
	removeMarker := strings.Index(script, "rm -f '"+agentUpgradeMarker+"'")
	stopTimer := strings.Index(script, "systemctl stop goveto-edge-agent-rollback.timer")
	if removeMarker < 0 || stopTimer < removeMarker {
		t.Fatalf("disarm must remove the rollback marker before stopping the timer: %s", script)
	}
}

func TestAgentUpgradeWatchdogRestoresBeforeRestart(t *testing.T) {
	restore := strings.Index(agentUpgradeRollbackScriptBody, "install -m 0755")
	restart := strings.Index(agentUpgradeRollbackScriptBody, "systemctl restart goveto-edge-agent")
	clear := strings.Index(agentUpgradeRollbackScriptBody, "rm -f \"$marker\"")
	if restore < 0 || restart < restore || clear < restart {
		t.Fatalf("rollback watchdog ordering is unsafe: %s", agentUpgradeRollbackScriptBody)
	}
}
