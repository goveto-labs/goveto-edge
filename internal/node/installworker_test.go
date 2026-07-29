package node

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os/exec"
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
	capabilityCheck := "if [ -e /proc/sys/net/core/rmem_max ] && [ -e /proc/sys/net/core/wmem_max ]; then"
	applyRmem := "sudo sysctl -w net.core.rmem_max=7500000"
	applyWmem := "sudo sysctl -w net.core.wmem_max=7500000"
	rmem := "echo 'net.core.rmem_max=7500000' | sudo tee /etc/sysctl.d/99-udp-buffers.conf >/dev/null"
	wmem := "echo 'net.core.wmem_max=7500000' | sudo tee -a /etc/sysctl.d/99-udp-buffers.conf >/dev/null"
	for _, command := range []string{capabilityCheck, applyRmem, applyWmem, rmem, wmem} {
		if !strings.Contains(script, command) {
			t.Fatalf("install script missing UDP buffer command %q: %s", command, script)
		}
	}
	if strings.Index(script, capabilityCheck) > strings.Index(script, applyRmem) ||
		strings.Index(script, applyRmem) > strings.Index(script, applyWmem) ||
		strings.Index(script, applyWmem) > strings.Index(script, rmem) ||
		strings.Index(script, rmem) > strings.Index(script, wmem) {
		t.Fatalf("install script configures UDP buffers in the wrong order: %s", script)
	}
	for _, warning := range []string{
		"warning: UDP buffer sysctl settings are unavailable; continuing agent installation",
		"warning: unable to raise UDP buffer limits; continuing agent installation",
		"warning: UDP buffer settings applied but could not be persisted",
	} {
		if !strings.Contains(script, warning) {
			t.Fatalf("install script missing non-fatal warning %q: %s", warning, script)
		}
	}
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
	if strings.Index(script, applyWmem) > strings.Index(script, "sudo systemctl restart goveto-edge-agent") {
		t.Fatalf("install script applies UDP buffers after restarting the service: %s", script)
	}
}

func TestAgentInstallScriptHasValidShellSyntax(t *testing.T) {
	for _, privileged := range []string{"", "sudo "} {
		cmd := exec.Command("sh", "-n")
		cmd.Stdin = strings.NewReader(agentInstallScript(privileged))
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("install script with prefix %q has invalid shell syntax: %v: %s", privileged, err, output)
		}
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
