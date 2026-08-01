package edgeagent

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

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
