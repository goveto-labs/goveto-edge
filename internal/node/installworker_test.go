package node

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestInstallRejectsMissingSSHInput(t *testing.T) {
	worker := &InstallWorker{}
	err := worker.install(context.Background(), InstallPayload{NodeID: "node-1"})
	if err == nil || err.Error() != "SSH installation input is missing" {
		t.Fatalf("install() error = %v", err)
	}
}

func TestInstallJobTimeoutCoversExecutionLimit(t *testing.T) {
	if installJobTimeout <= installExecutionTimeout {
		t.Fatalf("job timeout %s does not cover install limit %s and preparation", installJobTimeout, installExecutionTimeout)
	}
}

func TestReconcileTerminalJobsIsDeterministicAndCancellationIsNotFailure(t *testing.T) {
	for _, fragment := range []string{
		"ORDER BY node_id, updated_at DESC, id DESC",
		`CASE WHEN j.status='CANCELLED' THEN 'PENDING' ELSE 'INSTALL_FAILED' END)::"NodeStatus"`,
		"CASE WHEN j.status='CANCELLED' THEN NULL",
	} {
		if !strings.Contains(reconcileTerminalInstallJobsSQL, fragment) {
			t.Fatalf("reconcile SQL missing %q: %s", fragment, reconcileTerminalInstallJobsSQL)
		}
	}
}

func TestShellQuote(t *testing.T) {
	if got, want := shellQuote("it's safe"), `'it'\''s safe'`; got != want {
		t.Fatalf("shellQuote() = %q, want %q", got, want)
	}
}

func TestNormalizeArchitecture(t *testing.T) {
	for input, want := range map[string]string{"x86_64\n": "amd64", "amd64": "amd64", "aarch64\n": "arm64", "arm64": "arm64"} {
		got, err := normalizeArchitecture(input)
		if err != nil || got != want {
			t.Fatalf("normalizeArchitecture(%q) = %q, %v", input, got, err)
		}
	}
	if _, err := normalizeArchitecture("riscv64"); err == nil {
		t.Fatal("expected unsupported architecture error")
	} else if !errors.Is(err, errPermanentInstallConfiguration) {
		t.Fatalf("unsupported architecture should be permanent: %v", err)
	}
}

func TestCommandOutputSuffix(t *testing.T) {
	if got := commandOutputSuffix([]byte("\npermission denied\n")); got != ": permission denied" {
		t.Fatalf("commandOutputSuffix() = %q", got)
	}
	if got := commandOutputSuffix(nil); got != "" {
		t.Fatalf("empty commandOutputSuffix() = %q", got)
	}
}

func TestPrivilegedCommandPrefix(t *testing.T) {
	if got := privilegedCommandPrefix("root"); got != "" {
		t.Fatalf("root prefix=%q", got)
	}
	if got := privilegedCommandPrefix("ubuntu"); got != "sudo " {
		t.Fatalf("non-root prefix=%q", got)
	}
}

func TestAgentInstallScriptRestartsExistingService(t *testing.T) {
	script := agentInstallScript("sudo ")
	if !strings.Contains(script, "sudo systemctl enable goveto-edge-agent") {
		t.Fatalf("install script does not enable service: %s", script)
	}
	if !strings.Contains(script, "sudo systemctl restart goveto-edge-agent") {
		t.Fatalf("install script does not restart service: %s", script)
	}
	if !strings.Contains(script, "sudo systemctl is-active --quiet goveto-edge-agent") {
		t.Fatalf("install script does not verify active service: %s", script)
	}
	if strings.Contains(script, "enable --now") {
		t.Fatalf("install script still relies on enable --now: %s", script)
	}
}

func TestReadSCPAck(t *testing.T) {
	if err := readSCPAck(bufio.NewReader(bytes.NewReader([]byte{0}))); err != nil {
		t.Fatal(err)
	}
	err := readSCPAck(bufio.NewReader(strings.NewReader("\x01scp unavailable\n")))
	if err == nil || !strings.Contains(err.Error(), "scp unavailable") {
		t.Fatalf("unexpected SCP error: %v", err)
	}
}
