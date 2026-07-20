package cacheheaders

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

func TestCacheResult(t *testing.T) {
	for input, want := range map[string]string{
		"Goveto; hit; ttl=10":          "HIT",
		"Goveto; fwd=uri-miss; stored": "MISS",
		"Goveto; fwd=stale":            "STALE",
		"":                             "BYPASS",
	} {
		if got := cacheResult(input); got != want {
			t.Fatalf("cacheResult(%q)=%q want %q", input, got, want)
		}
	}
}

type cookieHandler struct{}

func (cookieHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) error {
	w.Header().Set("Set-Cookie", "session=secret")
	w.WriteHeader(http.StatusOK)
	return nil
}

func TestHandlerProtectsSetCookieResponses(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	handler := Handler{DefaultTTL: 300}
	if err := handler.ServeHTTP(recorder, request, caddyhttp.Handler(cookieHandler{})); err != nil {
		t.Fatal(err)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("Cache-Control=%q", got)
	}
}

func TestHandlerAddsStaleIfErrorToGeneratedCacheControl(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	handler := Handler{DefaultTTL: 300, StaleIfErrorTTL: 60}
	if err := handler.ServeHTTP(recorder, request, caddyhttp.Handler(emptyHandler{})); err != nil {
		t.Fatal(err)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "public, max-age=300, stale-if-error=60" {
		t.Fatalf("Cache-Control=%q", got)
	}
}

type emptyHandler struct{}

func (emptyHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) error {
	w.WriteHeader(http.StatusOK)
	return nil
}
