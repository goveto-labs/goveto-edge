package httpsecurity

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/redis/go-redis/v9"
)

func runMiddleware(t *testing.T, request *http.Request, options Options, handler echo.HandlerFunc) (*httptest.ResponseRecorder, error) {
	t.Helper()
	recorder := httptest.NewRecorder()
	c := echo.New().NewContext(request, recorder)
	return recorder, Middleware(options)(handler)(c)
}

func TestMiddlewareSetsSecurityHeadersAndRequestID(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	request.Header.Set(RequestIDHeader, "request-123")
	recorder, err := runMiddleware(t, request, Options{HSTS: true}, func(c *echo.Context) error {
		return c.NoContent(http.StatusNoContent)
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		RequestIDHeader: "request-123", "X-Content-Type-Options": "nosniff",
		"X-Frame-Options": "DENY", "Strict-Transport-Security": "max-age=31536000; includeSubDomains",
	} {
		if got := recorder.Header().Get(name); got != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestMiddlewareRejectsMissingCSRFForAuthenticatedMutation(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", strings.NewReader(`{}`))
	request.AddCookie(&http.Cookie{Name: "session", Value: "token"})
	request.AddCookie(&http.Cookie{Name: "session_csrf", Value: "csrf"})
	_, err := runMiddleware(t, request, Options{SessionCookieName: "session", CSRFCookieName: "session_csrf"},
		func(*echo.Context) error { return nil })
	var httpError *echo.HTTPError
	if !errors.As(err, &httpError) || httpError.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF error = %v", err)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", strings.NewReader(`{}`))
	request.AddCookie(&http.Cookie{Name: "session", Value: "token"})
	request.AddCookie(&http.Cookie{Name: "session_csrf", Value: "csrf"})
	request.Header.Set("X-CSRF-Token", "csrf")
	_, err = runMiddleware(t, request, Options{SessionCookieName: "session", CSRFCookieName: "session_csrf"},
		func(*echo.Context) error { return nil })
	if err != nil {
		t.Fatalf("valid CSRF rejected: %v", err)
	}
}

func TestMiddlewareRejectsCrossOriginAndOversizedRequests(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "https://control.example/api/v1/auth/login", strings.NewReader(`{}`))
	request.Host = "control.example"
	request.Header.Set("Origin", "https://attacker.example")
	_, err := runMiddleware(t, request, Options{}, func(*echo.Context) error { return nil })
	if statusFromError(err) != http.StatusForbidden {
		t.Fatalf("cross-origin error = %v", err)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader("12345"))
	_, err = runMiddleware(t, request, Options{MaxBodyBytes: 4, MaxUploadBytes: 4}, func(*echo.Context) error { return nil })
	if statusFromError(err) != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body error = %v", err)
	}
}

func TestMiddlewareRecoversPanic(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/panic", nil)
	recorder, err := runMiddleware(t, request, Options{}, func(*echo.Context) error { panic("boom") })
	if statusFromError(err) != http.StatusInternalServerError || recorder.Header().Get(RequestIDHeader) == "" {
		t.Fatalf("panic result = %v, request id = %q", err, recorder.Header().Get(RequestIDHeader))
	}
}

type fakeRateStore struct {
	mu     sync.Mutex
	counts map[string]int64
}

func (s *fakeRateStore) Incr(_ context.Context, key string) *redis.IntCmd {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.counts == nil {
		s.counts = map[string]int64{}
	}
	s.counts[key]++
	return redis.NewIntResult(s.counts[key], nil)
}

func (*fakeRateStore) Expire(context.Context, string, time.Duration) *redis.BoolCmd {
	return redis.NewBoolResult(true, nil)
}

func TestRateLimiterEnforcesQuota(t *testing.T) {
	limiter := &RateLimiter{redis: &fakeRateStore{}}
	handler := limiter.Limit("login", 2, time.Minute)(func(*echo.Context) error { return nil })
	for attempt := 1; attempt <= 3; attempt++ {
		request := httptest.NewRequest(http.MethodPost, "/login", nil)
		request.RemoteAddr = "192.0.2.1:1234"
		c := echo.New().NewContext(request, httptest.NewRecorder())
		err := handler(c)
		if attempt <= 2 && err != nil {
			t.Fatalf("attempt %d rejected: %v", attempt, err)
		}
		if attempt == 3 && statusFromError(err) != http.StatusTooManyRequests {
			t.Fatalf("attempt 3 error = %v", err)
		}
	}
}

func TestTrustedProxyIPExtractorTrustsOnlyConfiguredRanges(t *testing.T) {
	if extractor, err := TrustedProxyIPExtractor(nil); err != nil || extractor != nil {
		t.Fatalf("no proxies should mean no extractor, got %v, %v", extractor, err)
	}
	if _, err := TrustedProxyIPExtractor([]string{"not-a-cidr"}); err == nil {
		t.Fatal("invalid CIDR was accepted")
	}

	extractor, err := TrustedProxyIPExtractor([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "10.1.2.3:4444"
	request.Header.Set("X-Forwarded-For", "203.0.113.9")
	if got := extractor(request); got != "203.0.113.9" {
		t.Fatalf("trusted proxy header ignored, got %q", got)
	}

	// A connection from outside the trusted range cannot spoof its IP,
	// even from a private address (echo's defaults are disabled).
	request = httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "192.168.7.7:4444"
	request.Header.Set("X-Forwarded-For", "203.0.113.9")
	if got := extractor(request); got != "192.168.7.7" {
		t.Fatalf("untrusted proxy header trusted, got %q", got)
	}
}

func TestLimitKeyedBucketsPerKey(t *testing.T) {
	limiter := &RateLimiter{redis: &fakeRateStore{}}
	user := "user-a"
	handler := limiter.LimitKeyed("sensitive", 1, time.Minute, func(*echo.Context) string { return user })(
		func(*echo.Context) error { return nil })

	newContext := func() *echo.Context {
		return echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/password", nil), httptest.NewRecorder())
	}
	if err := handler(newContext()); err != nil {
		t.Fatalf("first request rejected: %v", err)
	}
	if err := handler(newContext()); statusFromError(err) != http.StatusTooManyRequests {
		t.Fatalf("second request error = %v", err)
	}
	user = "user-b"
	if err := handler(newContext()); err != nil {
		t.Fatalf("other user shares the bucket: %v", err)
	}
}
