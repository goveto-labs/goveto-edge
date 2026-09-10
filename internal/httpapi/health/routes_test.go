package health

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/buildinfo"
)

func TestLiveIncludesBuildVersion(t *testing.T) {
	request := httptest.NewRequest("GET", "/health/live", nil)
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(request, recorder)
	if err := live(ctx); err != nil {
		t.Fatal(err)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `"status":"ok"`) || !strings.Contains(body, `"version":"`+buildinfo.Current()+`"`) {
		t.Fatalf("liveness response does not include status and version: %s", body)
	}
	if !strings.Contains(body, `"startedAt":"`) {
		t.Fatalf("liveness response does not include startedAt: %s", body)
	}
}
