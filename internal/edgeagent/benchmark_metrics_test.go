package edgeagent

import (
	"net/http"
	"net/http/httptest"
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
