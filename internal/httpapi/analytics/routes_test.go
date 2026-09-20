package analytics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/analytics"
)

// lockedRecorder serializes handler writes against test-side reads.
type lockedRecorder struct {
	mu  sync.Mutex
	rec *httptest.ResponseRecorder
}

func (l *lockedRecorder) Header() http.Header { return l.rec.Header() }

func (l *lockedRecorder) WriteHeader(code int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rec.WriteHeader(code)
}

func (l *lockedRecorder) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rec.Write(p)
}

func (l *lockedRecorder) Flush() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rec.Flush()
}

func (l *lockedRecorder) body() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rec.Body.String()
}

func (l *lockedRecorder) status() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rec.Code
}

func TestLiveLogsIgnoresInvalidCursor(t *testing.T) {
	store := analytics.NewStore(nil, 0)
	e := echo.New()
	e.GET("/api/v1/clusters/:cluster_id/analytics/logs/stream", liveLogs(store))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/clusters/cluster-1/analytics/logs/stream", nil)
	req.Header.Set("Last-Event-ID", "bogus-cursor")
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	rec := &lockedRecorder{rec: httptest.NewRecorder()}

	done := make(chan struct{})
	go func() { defer close(done); e.ServeHTTP(rec, req.WithContext(ctx)) }()
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(rec.body(), "event: ready") {
		if time.Now().After(deadline) {
			t.Fatalf("invalid cursor was not ignored; body=%q", rec.body())
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if code := rec.status(); code != http.StatusOK {
		t.Fatalf("status=%d body=%q", code, rec.body())
	}
}
