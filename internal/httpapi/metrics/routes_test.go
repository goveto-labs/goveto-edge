package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
)

func TestMetricsEndpointExposesCoreFamilies(t *testing.T) {
	e := echo.New()
	e.Use(Middleware())
	Register(e)
	e.GET("/api/v1/ping", func(c *echo.Context) error { return c.String(http.StatusOK, "pong") })

	request := httptest.NewRequest(http.MethodGet, "/api/v1/ping", nil)
	response := httptest.NewRecorder()
	e.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status %d", response.Code)
	}

	scrape := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	scrapeResponse := httptest.NewRecorder()
	e.ServeHTTP(scrapeResponse, scrape)
	if scrapeResponse.Code != http.StatusOK {
		t.Fatalf("unexpected /metrics status %d", scrapeResponse.Code)
	}
	body := scrapeResponse.Body.String()
	for _, family := range []string{
		"goveto_control_http_requests_total",
		"goveto_control_http_request_duration_seconds",
		"go_goroutines",
	} {
		if !strings.Contains(body, family) {
			t.Errorf("metrics output missing %s", family)
		}
	}
	if !strings.Contains(body, `route="/api/v1/ping"`) {
		t.Errorf("metrics output missing route template label, body excerpt: %.400s", body)
	}
	if strings.Contains(body, "/api/v1/ping?") {
		t.Errorf("metrics must not contain raw request paths")
	}
}

func TestResponseStatusFromError(t *testing.T) {
	e := echo.New()
	e.Use(Middleware())
	Register(e)
	e.GET("/api/v1/fail", func(c *echo.Context) error {
		return echo.NewHTTPError(http.StatusForbidden, "nope")
	})

	request := httptest.NewRequest(http.MethodGet, "/api/v1/fail", nil)
	response := httptest.NewRecorder()
	e.ServeHTTP(response, request)

	scrape := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	scrapeResponse := httptest.NewRecorder()
	e.ServeHTTP(scrapeResponse, scrape)
	if !strings.Contains(scrapeResponse.Body.String(), `code="403"`) {
		t.Errorf("expected 403 status label for HTTPError, body excerpt: %.400s", scrapeResponse.Body.String())
	}
}

func TestMetricMethodBoundsLabelCardinality(t *testing.T) {
	for _, method := range []string{
		http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodHead, http.MethodOptions, http.MethodTrace,
		http.MethodConnect,
	} {
		if got := metricMethod(method); got != method {
			t.Errorf("metricMethod(%q) = %q", method, got)
		}
	}
	for _, method := range []string{"", "CUSTOM", "ATTACK-123"} {
		if got := metricMethod(method); got != "OTHER" {
			t.Errorf("metricMethod(%q) = %q, want OTHER", method, got)
		}
	}
}
