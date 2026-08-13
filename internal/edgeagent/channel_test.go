package edgeagent

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

func TestNodeCacheTaskPersistsOnlyAfterCaddyAcceptsConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.json")
	store := NewNodeConfigStore(path)
	manager := NewConfigManager("", ":0")
	loadErr := errors.New("injected caddy load failure")
	manager.loadCaddy = func([]byte, bool) error { return loadErr }
	client := &channelClient{configs: manager, nodeConfigs: store}
	desired := NodeConfig{
		CacheDirectory:      "/srv/goveto-cache",
		AutoMaxSize:         false,
		MaxSizeBytes:        1 << 30,
		MaxDiskUsagePercent: 75,
	}
	payload, err := json.Marshal(desired)
	if err != nil {
		t.Fatal(err)
	}
	task := edgeprotocol.AgentTask{ID: "cache", Kind: edgeprotocol.TaskNodeCacheConfig, Payload: payload}

	result := client.executeTask(context.Background(), task)
	if result.Success || !strings.Contains(result.Error, loadErr.Error()) {
		t.Fatalf("failed load result = %#v", result)
	}
	if got := store.Get(); !reflect.DeepEqual(got, defaultNodeConfig()) {
		t.Fatalf("heartbeat config changed after failed load: %#v", got)
	}
	if !reflect.DeepEqual(manager.nodeConfig, defaultNodeConfig()) {
		t.Fatalf("manager config changed after failed load: %#v", manager.nodeConfig)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("failed config was persisted: %v", err)
	}

	manager.loadCaddy = func([]byte, bool) error { return nil }
	result = client.executeTask(context.Background(), task)
	if !result.Success {
		t.Fatalf("retry result = %#v", result)
	}
	if got := store.Get(); !reflect.DeepEqual(got, desired) {
		t.Fatalf("heartbeat config after retry = %#v; want %#v", got, desired)
	}
	if got := NewNodeConfigStore(path).Get(); !reflect.DeepEqual(got, desired) {
		t.Fatalf("persisted config after retry = %#v; want %#v", got, desired)
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
