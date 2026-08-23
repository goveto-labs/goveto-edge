package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/httpapi"
	"goveto-edge/internal/httpapi/types"
)

func newConsoleTestServer(t *testing.T) *echo.Echo {
	t.Helper()
	e := echo.New()
	e.HTTPErrorHandler = types.HTTPErrorHandler
	// A representative API route plus a POST-only route to verify method
	// semantics are preserved next to the console catch-all.
	e.GET("/api/v1/clusters", func(c *echo.Context) error { return types.JSON(c, http.StatusOK, nil) })
	e.POST("/api/v1/clusters", func(c *echo.Context) error { return types.JSON(c, http.StatusCreated, nil) })
	e.GET("/health/live", func(c *echo.Context) error { return types.JSON(c, http.StatusOK, nil) })
	httpapi.RegisterConsoleFS(e, fstest.MapFS{
		"index.html":         {Data: []byte("<html>console-shell</html>")},
		"favicon.ico":        {Data: []byte("icon")},
		"assets/app-HASH.js": {Data: []byte("console-js")},
	})
	return e
}

func TestConsoleServesRootAndAssets(t *testing.T) {
	e := newConsoleTestServer(t)

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "console-shell") {
		t.Fatalf("GET / = %d %q", rec.Code, rec.Body.String())
	}
	if cache := rec.Header().Get("Cache-Control"); cache != "no-cache" {
		t.Fatalf("GET / Cache-Control = %q; want no-cache", cache)
	}

	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/app-HASH.js", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "console-js" {
		t.Fatalf("GET /assets/app-HASH.js = %d %q", rec.Code, rec.Body.String())
	}
	if cache := rec.Header().Get("Cache-Control"); cache != "public, max-age=31536000, immutable" {
		t.Fatalf("asset Cache-Control = %q; want immutable", cache)
	}
	if contentType := rec.Header().Get("Content-Type"); !strings.Contains(contentType, "javascript") {
		t.Fatalf("asset Content-Type = %q", contentType)
	}

	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/", nil))
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("HEAD / = %d (body %d bytes)", rec.Code, rec.Body.Len())
	}
}

func TestConsoleHistoryFallback(t *testing.T) {
	e := newConsoleTestServer(t)
	for _, deepLink := range []string{"/sites", "/sites/42/config", "/clusters/x/nodes/y"} {
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, deepLink, nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "console-shell") {
			t.Fatalf("GET %s = %d %q; want the SPA shell", deepLink, rec.Code, rec.Body.String())
		}
		if cache := rec.Header().Get("Cache-Control"); cache != "no-cache" {
			t.Fatalf("GET %s Cache-Control = %q; want no-cache", deepLink, cache)
		}
	}
}

func TestConsoleKeepsAPISemantics(t *testing.T) {
	e := newConsoleTestServer(t)

	// Unknown API paths must stay machine-readable JSON 404s.
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/clusters/unknown/child", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET unknown API path = %d; want 404", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, "console-shell") || !strings.Contains(body, `"code"`) {
		t.Fatalf("GET unknown API path body = %q; want a JSON error", body)
	}

	for _, reserved := range []string{"/health", "/health/unknown", "/metrics"} {
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, reserved, nil))
		if rec.Code == http.StatusOK && strings.Contains(rec.Body.String(), "console-shell") {
			t.Fatalf("GET %s served the SPA shell; want API semantics", reserved)
		}
	}

	// Registered API routes keep working and win over the catch-all.
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/clusters", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/clusters = %d", rec.Code)
	}

	// Method semantics: DELETE on a route that only allows GET/POST.
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/clusters", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE /api/v1/clusters = %d; want 405", rec.Code)
	}
}

func TestConsoleSkipsDotFilesAndMissingAssets(t *testing.T) {
	e := newConsoleTestServer(t)

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.gitkeep", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "console-shell") {
		t.Fatalf("GET /.gitkeep = %d %q; want the SPA shell, not the raw file", rec.Code, rec.Body.String())
	}

	// A missing hashed asset must not fall back to HTML: fetch/XHR consumers
	// of old asset URLs need a real 404.
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/missing-HASH.js", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /assets/missing-HASH.js = %d; want 404", rec.Code)
	}
}
