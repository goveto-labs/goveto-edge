package edgeagent

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"path/filepath"
	"testing"
	"time"

	"goveto-edge/internal/edgeprotocol"
)

func TestExecuteTaskRejectsUnsupportedKind(t *testing.T) {
	client := &channelClient{}
	result := client.executeTask(context.Background(), edgeprotocol.AgentTask{
		ID: "task-1", Kind: "UNKNOWN", Payload: json.RawMessage(`{}`),
	})
	if result.Success || result.Error == "" || result.TaskID != "task-1" {
		t.Fatalf("unexpected unsupported task result: %#v", result)
	}
}

func TestExecuteTaskRejectsInvalidPayloads(t *testing.T) {
	client := &channelClient{
		configs:     NewConfigManager("", ":0"),
		nodeConfigs: NewNodeConfigStore(filepath.Join(t.TempDir(), "node.json")),
	}
	for _, task := range []edgeprotocol.AgentTask{
		{ID: "apply", Kind: edgeprotocol.TaskApplySiteConfig, Payload: json.RawMessage(`{`)},
		{ID: "purge", Kind: edgeprotocol.TaskPurgeSite, Payload: json.RawMessage(`{`)},
		{ID: "cache", Kind: edgeprotocol.TaskNodeCacheConfig, Payload: json.RawMessage(`{`)},
	} {
		result := client.executeTask(context.Background(), task)
		if result.Success || result.Error == "" || result.TaskID != task.ID {
			t.Fatalf("task %s: unexpected result %#v", task.ID, result)
		}
	}
}

func TestNewCredentialRequestMatchesNodeIdentity(t *testing.T) {
	nodeID := "550e8400-e29b-41d4-a716-446655440000"
	request, privateKey, err := newCredentialRequest(nodeID)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode([]byte(request.CSRPEM))
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		t.Fatal("expected certificate request PEM")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := csr.CheckSignature(); err != nil {
		t.Fatal(err)
	}
	if csr.Subject.CommonName != nodeID {
		t.Fatalf("csr cn = %q", csr.Subject.CommonName)
	}
	publicKey, ok := csr.PublicKey.(ed25519.PublicKey)
	if !ok {
		t.Fatal("csr public key is not ed25519")
	}
	if !publicKey.Equal(privateKey.Public()) {
		t.Fatal("csr public key does not match private key")
	}
}

func TestSendClientMessageHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	target := make(chan *edgeprotocol.ClientMessage)
	if err := sendClientMessage(ctx, target, &edgeprotocol.ClientMessage{}); err == nil {
		t.Fatal("expected canceled context error")
	}
}

func TestReconnectDelayCapsExponentialBackoff(t *testing.T) {
	if reconnectDelay(0) != time.Second {
		t.Fatalf("attempt 0 = %s", reconnectDelay(0))
	}
	if reconnectDelay(5) != 32*time.Second {
		t.Fatalf("attempt 5 = %s", reconnectDelay(5))
	}
	if reconnectDelay(100) != 32*time.Second {
		t.Fatalf("attempt 100 = %s", reconnectDelay(100))
	}
}
