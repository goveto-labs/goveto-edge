package publish

import (
	"net/http/httptest"
	"testing"
)

func TestWriteSSE(t *testing.T) {
	recorder := httptest.NewRecorder()
	if err := writeSSE(recorder, "sync_status", map[string]any{"state": "syncing", "has_active_tasks": true}); err != nil {
		t.Fatal(err)
	}
	want := "event: sync_status\ndata: {\"has_active_tasks\":true,\"state\":\"syncing\"}\n\n"
	if got := recorder.Body.String(); got != want {
		t.Fatalf("SSE payload = %q, want %q", got, want)
	}
	if !recorder.Flushed {
		t.Fatal("SSE event was not flushed")
	}
}
