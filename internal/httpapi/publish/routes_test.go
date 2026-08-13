package publish

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/storage/gen/model"
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

func TestResourceGuardsReturnNotFound(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"missing site", requireSiteInCluster(nil, "cluster-1")},
		{"site in another cluster", requireSiteInCluster(&model.Site{ClusterId: "cluster-2"}, "cluster-1")},
		{"missing publish job", requirePublishJobInSite(nil, "site-1")},
		{"publish job for another site", requirePublishJobInSite(&model.PublishJob{SiteId: "site-2"}, "site-1")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var httpError *echo.HTTPError
			if !errors.As(test.err, &httpError) || httpError.Code != http.StatusNotFound {
				t.Fatalf("error = %#v, want HTTP 404", test.err)
			}
		})
	}
	if err := requireSiteInCluster(&model.Site{ClusterId: "cluster-1"}, "cluster-1"); err != nil {
		t.Fatalf("matching site rejected: %v", err)
	}
	if err := requirePublishJobInSite(&model.PublishJob{SiteId: "site-1"}, "site-1"); err != nil {
		t.Fatalf("matching publish job rejected: %v", err)
	}
}
