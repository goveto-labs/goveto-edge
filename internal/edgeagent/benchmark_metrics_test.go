package edgeagent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestBenchmarkMetricsExposeCacheWriteTelemetry(t *testing.T) {
	handler := benchmarkMetricsHandler(nil, NewNodeConfigStore(filepath.Join(t.TempDir(), "node.json")))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("metrics status=%d", response.Code)
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"cache_write_queue_depth", "cache_write_queue_bytes", "cache_write_batches", "cache_write_objects_committed", "cache_write_commit_latency_ms", "cache_inflight_writes"} {
		if _, ok := payload[name]; !ok {
			t.Errorf("metrics payload is missing %q", name)
		}
	}
}

func TestBenchmarkCacheControlEndpoints(t *testing.T) {
	handler := benchmarkMetricsHandler(nil, NewNodeConfigStore(filepath.Join(t.TempDir(), "node.json")))
	for _, path := range []string{"/cache/drain", "/cache/reset"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, nil))
		if response.Code != http.StatusNoContent {
			t.Fatalf("POST %s status=%d body=%s", path, response.Code, response.Body.String())
		}
		get := httptest.NewRecorder()
		handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, path, nil))
		if get.Code != http.StatusMethodNotAllowed {
			t.Fatalf("GET %s status=%d", path, get.Code)
		}
	}
}

func TestBenchmarkMetricsGCRequiresPostAndCompletes(t *testing.T) {
	handler := benchmarkMetricsHandler(nil, nil)
	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/gc", nil))
	if get.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /gc status=%d", get.Code)
	}
	post := httptest.NewRecorder()
	handler.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/gc", nil))
	if post.Code != http.StatusNoContent {
		t.Fatalf("POST /gc status=%d", post.Code)
	}
}

func TestBenchmarkVariantEndpointControlsAccessPipeline(t *testing.T) {
	queue, err := OpenLogQueue(filepath.Join(t.TempDir(), "logs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	handler := benchmarkMetricsHandler(queue, NewNodeConfigStore(filepath.Join(t.TempDir(), "node.json")))
	for _, variant := range []string{"control", "full"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/variant?value="+variant, nil))
		if response.Code != http.StatusNoContent {
			t.Fatalf("variant %s status=%d body=%s", variant, response.Code, response.Body.String())
		}
		if queue.benchmarkAccessLogs.Load() != (variant == "full") {
			t.Fatalf("variant %s did not update access pipeline", variant)
		}
	}
	queue.setBenchmarkAccessLogsEnabled(true)
}
