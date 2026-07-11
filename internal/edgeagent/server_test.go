package edgeagent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"goveto-edge/internal/edgeprotocol"
)

func TestAgentServerHealthAndNodeConfig(t *testing.T) {
	identity := Identity{NodeID: "550e8400-e29b-41d4-a716-446655440000", CommunicationKey: "secret"}
	dir := t.TempDir()
	configs := NewConfigManager(filepath.Join(dir, "sites.json"), ":"+strconv.Itoa(freePort(t)))
	configs.SetAgentHost(identity.NodeID)
	nodeConfigs := NewNodeConfigStore(filepath.Join(dir, "node.json"))
	logs, err := OpenLogQueue(filepath.Join(dir, "logs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer logs.Close()
	auth := newAuthenticator(identity, filepath.Join(dir, "nonces.json"))
	handler := newAgentServer(identity, configs, nodeConfigs, logs, auth)

	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "http://node/v1/health", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health status: %d", health.Code)
	}
	var healthBody map[string]any
	if err := json.Unmarshal(health.Body.Bytes(), &healthBody); err != nil {
		t.Fatal(err)
	}
	if healthBody["node_id"] != identity.NodeID || healthBody["status"] != "ok" {
		t.Fatalf("health body: %#v", healthBody)
	}

	body := `{"cache_directory":"/tmp/cache","auto_max_size":false,"max_size_bytes":123,"max_disk_usage_percent":70}`
	request := signedRequest(identity, http.MethodPut, "/v1/node/config", []byte(body), "nonce-node-config")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("node config status: %d body=%s", response.Code, response.Body.String())
	}
	got := nodeConfigs.Get()
	if got.CacheDirectory != "/tmp/cache" || got.MaxSizeBytes != 123 || got.MaxDiskUsagePercent != 70 || got.AutoMaxSize {
		t.Fatalf("node config not applied: %#v", got)
	}
	reloaded := NewNodeConfigStore(filepath.Join(dir, "node.json"))
	if reloaded.Get().CacheDirectory != "/tmp/cache" {
		t.Fatalf("node config not persisted: %#v", reloaded.Get())
	}
}

func TestAgentServerLogsPullAndAck(t *testing.T) {
	identity := Identity{NodeID: "550e8400-e29b-41d4-a716-446655440000", CommunicationKey: "secret"}
	dir := t.TempDir()
	configs := NewConfigManager("", ":"+strconv.Itoa(freePort(t)))
	configs.SetAgentHost(identity.NodeID)
	nodeConfigs := NewNodeConfigStore(filepath.Join(dir, "node.json"))
	logs, err := OpenLogQueue(filepath.Join(dir, "logs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer logs.Close()
	id, err := logs.Append(LogRecord{Type: "access", Payload: json.RawMessage(`{"status":200}`)})
	if err != nil {
		t.Fatal(err)
	}
	handler := newAgentServer(identity, configs, nodeConfigs, logs, newAuthenticator(identity, ""))

	pull := signedRequest(identity, http.MethodGet, "/v1/logs/pull?wait=1&limit=10", nil, "nonce-pull")
	pullResponse := httptest.NewRecorder()
	handler.ServeHTTP(pullResponse, pull)
	if pullResponse.Code != http.StatusOK {
		t.Fatalf("pull status: %d body=%s", pullResponse.Code, pullResponse.Body.String())
	}
	var payload struct {
		NodeID  string      `json:"node_id"`
		Records []LogRecord `json:"records"`
	}
	if err := json.Unmarshal(pullResponse.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.NodeID != identity.NodeID || len(payload.Records) != 1 || payload.Records[0].ID != id {
		t.Fatalf("pull payload: %#v", payload)
	}

	ackBody := []byte(`{"through":` + itoa(id) + `}`)
	ack := signedRequest(identity, http.MethodPost, "/v1/logs/ack", ackBody, "nonce-ack")
	ackResponse := httptest.NewRecorder()
	handler.ServeHTTP(ackResponse, ack)
	if ackResponse.Code != http.StatusNoContent {
		t.Fatalf("ack status: %d body=%s", ackResponse.Code, ackResponse.Body.String())
	}
	batch, err := logs.Batch(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 0 {
		t.Fatalf("records remain after ack: %#v", batch)
	}
}

func TestAgentServerRejectsSiteIDMismatch(t *testing.T) {
	identity := Identity{NodeID: "550e8400-e29b-41d4-a716-446655440000", CommunicationKey: "secret"}
	dir := t.TempDir()
	configs := NewConfigManager(filepath.Join(dir, "sites.json"), ":"+strconv.Itoa(freePort(t)))
	configs.SetAgentHost(identity.NodeID)
	logs, err := OpenLogQueue(filepath.Join(dir, "logs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer logs.Close()
	handler := newAgentServer(identity, configs, NewNodeConfigStore(filepath.Join(dir, "node.json")), logs, newAuthenticator(identity, ""))

	config := validHTTPConfig(t)
	body, _ := json.Marshal(config)
	request := signedRequest(identity, http.MethodPut, "/v1/sites/other-site/config", body, "nonce-mismatch")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected site id mismatch, got %d body=%s", response.Code, response.Body.String())
	}
}

func TestAgentServerAppliesSiteConfig(t *testing.T) {
	ensureAgentLogSink(t)
	identity := Identity{NodeID: "550e8400-e29b-41d4-a716-446655440000", CommunicationKey: "secret"}
	dir := t.TempDir()
	configs := NewConfigManager(filepath.Join(dir, "sites.json"), ":"+strconv.Itoa(freePort(t)))
	configs.SetAgentHost(identity.NodeID)
	logs, err := OpenLogQueue(filepath.Join(dir, "logs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer logs.Close()
	handler := newAgentServer(identity, configs, NewNodeConfigStore(filepath.Join(dir, "node.json")), logs, newAuthenticator(identity, filepath.Join(dir, "nonces.json")))

	config := validHTTPConfig(t)
	body, _ := json.Marshal(config)
	request := signedRequest(identity, http.MethodPut, "/v1/sites/"+config.SiteID+"/config", body, "nonce-apply")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("apply status: %d body=%s", response.Code, response.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["applied"] != true || result["version"] != float64(1) {
		t.Fatalf("apply result: %#v", result)
	}
	if configs.ConfigVersion() != 1 {
		t.Fatalf("config version: %d", configs.ConfigVersion())
	}
	_ = configs.Stop()
}

func TestAgentServerInvalidAck(t *testing.T) {
	identity := Identity{NodeID: "550e8400-e29b-41d4-a716-446655440000", CommunicationKey: "secret"}
	dir := t.TempDir()
	logs, err := OpenLogQueue(filepath.Join(dir, "logs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer logs.Close()
	handler := newAgentServer(identity, NewConfigManager("", ":"+strconv.Itoa(freePort(t))), NewNodeConfigStore(filepath.Join(dir, "node.json")), logs, newAuthenticator(identity, ""))
	request := signedRequest(identity, http.MethodPost, "/v1/logs/ack", []byte(`{"through":0}`), "nonce-bad-ack")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected bad ack, got %d", response.Code)
	}
}

func signedRequest(identity Identity, method, path string, body []byte, nonce string) *http.Request {
	if body == nil {
		body = []byte{}
	}
	request := httptest.NewRequest(method, "http://node"+path, strings.NewReader(string(body)))
	request.Host = identity.NodeID
	timestamp := time.Now().UTC().Format(time.RFC3339)
	hash := edgeprotocol.ContentHash(body)
	request.Header.Set(edgeprotocol.HeaderNodeID, identity.NodeID)
	request.Header.Set(edgeprotocol.HeaderTimestamp, timestamp)
	request.Header.Set(edgeprotocol.HeaderNonce, nonce)
	request.Header.Set(edgeprotocol.HeaderContentHash, hash)
	request.Header.Set(edgeprotocol.HeaderSignature, edgeprotocol.Sign(identity.CommunicationKey, method, identity.NodeID, request.URL.RequestURI(), timestamp, nonce, hash))
	request.Header.Set("Content-Type", "application/json")
	return request
}

func itoa(value uint64) string {
	if value == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for value > 0 {
		i--
		buf[i] = byte('0' + value%10)
		value /= 10
	}
	return string(buf[i:])
}
