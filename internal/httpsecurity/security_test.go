package httpsecurity

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/redis/go-redis/v9"

	"goveto-edge/internal/settings"
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

func TestProxyIPExtractorDisabledIgnoresForwardingHeaders(t *testing.T) {
	extractor, err := ProxyIPExtractor(settings.HTTPProxyConfig{
		ClientIPHeaders: []string{"X-Forwarded-For"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if extractor != nil {
		t.Fatal("disabled forwarding headers configured an IP extractor")
	}
}

func TestProxyIPExtractorTrustsAnySourceAndHonorsHeaderPriority(t *testing.T) {
	extractor, err := ProxyIPExtractor(settings.HTTPProxyConfig{
		TrustAll:        true,
		ClientIPHeaders: []string{"Cf-Connecting-IP", "X-Forwarded-For"},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "192.0.2.20:4444"
	request.Header.Set("Cf-Connecting-IP", "203.0.113.10")
	request.Header.Set("X-Forwarded-For", "198.51.100.4, 10.1.2.3")
	if got := extractor(request); got != "203.0.113.10" {
		t.Fatalf("client IP = %q, want first configured header", got)
	}
}

func TestProxyIPExtractorTrustAllAndForwarded(t *testing.T) {
	extractor, err := ProxyIPExtractor(settings.HTTPProxyConfig{
		TrustAll: true, ClientIPHeaders: []string{"Forwarded", "X-Real-IP"},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "192.0.2.20:4444"
	request.Header.Set("Forwarded", `for="[2001:db8::8]:4711";proto=https`)
	if got := extractor(request); got != "2001:db8::8" {
		t.Fatalf("Forwarded client IP = %q", got)
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

type failingRateStore struct{}

func (failingRateStore) Incr(context.Context, string) *redis.IntCmd {
	return redis.NewIntResult(0, errors.New("redis is down"))
}

func (failingRateStore) Expire(context.Context, string, time.Duration) *redis.BoolCmd {
	return redis.NewBoolResult(false, errors.New("redis is down"))
}

func rateLimitContext() *echo.Context {
	request := httptest.NewRequest(http.MethodPost, "/login", nil)
	request.RemoteAddr = "192.0.2.1:1234"
	return echo.New().NewContext(request, httptest.NewRecorder())
}

func TestRateLimiterFallsBackToMemoryWhenRedisFails(t *testing.T) {
	limiter := NewRateLimiter(nil)
	limiter.redis = failingRateStore{}
	handler := limiter.Limit("login", 2, time.Minute)(func(*echo.Context) error { return nil })
	for attempt := 1; attempt <= 3; attempt++ {
		c := rateLimitContext()
		err := handler(c)
		if attempt <= 2 && err != nil {
			t.Fatalf("attempt %d rejected during Redis outage: %v", attempt, err)
		}
		if attempt == 3 && statusFromError(err) != http.StatusTooManyRequests {
			t.Fatalf("attempt 3 error = %v, want quota enforced by the in-memory fallback", err)
		}
	}
	if !limiter.degraded.Load() {
		t.Fatal("limiter did not record the degraded state")
	}
}

func TestRateLimiterWithoutRedisUsesMemoryBackend(t *testing.T) {
	limiter := NewRateLimiter(nil)
	handler := limiter.Limit("login", 1, time.Minute)(func(*echo.Context) error { return nil })
	if err := handler(rateLimitContext()); err != nil {
		t.Fatalf("first request rejected without Redis: %v", err)
	}
	if err := handler(rateLimitContext()); statusFromError(err) != http.StatusTooManyRequests {
		t.Fatalf("second request error = %v", err)
	}
}

func TestRateLimiterRecoversToRedis(t *testing.T) {
	limiter := NewRateLimiter(nil)
	if err := limiter.Limit("login", 5, time.Minute)(func(*echo.Context) error { return nil })(rateLimitContext()); err != nil {
		t.Fatal(err)
	}
	if !limiter.degraded.Load() {
		t.Fatal("limiter should be degraded without Redis")
	}
	limiter.redis = &fakeRateStore{}
	if err := limiter.Limit("login", 5, time.Minute)(func(*echo.Context) error { return nil })(rateLimitContext()); err != nil {
		t.Fatal(err)
	}
	if limiter.degraded.Load() {
		t.Fatal("limiter did not recover to the Redis backend")
	}
}

func TestLocalRateStoreExpiresWindows(t *testing.T) {
	store := newLocalRateStore()
	now := time.Now().UTC()
	if count := store.incr("key", time.Minute, now); count != 1 {
		t.Fatalf("first count = %d", count)
	}
	if count := store.incr("key", time.Minute, now.Add(30*time.Second)); count != 2 {
		t.Fatalf("same-window count = %d", count)
	}
	if count := store.incr("key", time.Minute, now.Add(61*time.Second)); count != 1 {
		t.Fatalf("expired-window count = %d, want reset to 1", count)
	}
}

func TestLocalRateStoreCapsDistinctKeys(t *testing.T) {
	store := newLocalRateStore()
	now := time.Now().UTC()
	for index := 0; index < localRateStoreMaxKeys; index++ {
		key := fmt.Sprintf("key-%d", index)
		if count := store.incr(key, time.Minute, now); count != 1 {
			t.Fatalf("seed %s count=%d", key, count)
		}
	}
	if got := len(store.buckets); got != localRateStoreMaxKeys {
		t.Fatalf("bucket count=%d want %d", got, localRateStoreMaxKeys)
	}
	// Additional distinct keys must collapse into the overflow bucket instead of growing RAM.
	if count := store.incr("attacker-new", time.Minute, now); count != 1 {
		t.Fatalf("overflow first count=%d", count)
	}
	if count := store.incr("attacker-other", time.Minute, now); count != 2 {
		t.Fatalf("overflow shared count=%d", count)
	}
	if _, ok := store.buckets[localRateOverflowKey]; !ok {
		t.Fatal("overflow bucket missing")
	}
	if got := len(store.buckets); got != localRateStoreMaxKeys+1 {
		t.Fatalf("bucket count after overflow=%d", got)
	}
	// Existing keys remain independently tracked.
	if count := store.incr("key-0", time.Minute, now); count != 2 {
		t.Fatalf("existing key count=%d", count)
	}
}
