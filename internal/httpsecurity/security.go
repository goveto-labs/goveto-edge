// Package httpsecurity provides control API transport and browser protections.
package httpsecurity

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"github.com/redis/go-redis/v9"

	"goveto-edge/internal/settings"
)

const RequestIDHeader = "X-Request-ID"

type Options struct {
	SessionCookieName string
	CSRFCookieName    string
	MaxBodyBytes      int64
	MaxUploadBytes    int64
	MaxHeaderCount    int
	HSTS              bool
	// IPExtractor, when set, resolves the client IP from proxy headers; leave
	// nil when the server is reached directly so headers cannot spoof it.
	IPExtractor echo.IPExtractor
}

var forwardedForPattern = regexp.MustCompile(`(?i)(?:^|[;,]\s*)for=(?:"?\[?)([^\]";,]+)`)

// ProxyIPExtractor resolves a client address from the first configured header
// when forwarding headers are explicitly trusted from all sources.
func ProxyIPExtractor(config settings.HTTPProxyConfig) (echo.IPExtractor, error) {
	if err := config.NormalizeAndValidate(); err != nil {
		return nil, err
	}
	if !config.TrustAll {
		return nil, nil
	}
	return func(request *http.Request) string {
		direct := directRequestIP(request)
		for _, header := range config.ClientIPHeaders {
			if value := clientIPHeaderValue(request.Header, header); value != "" {
				return value
			}
		}
		return direct
	}, nil
}

func directRequestIP(request *http.Request) string {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err == nil {
		return host
	}
	return strings.Trim(request.RemoteAddr, "[]")
}

func clientIPHeaderValue(headers http.Header, name string) string {
	value := strings.TrimSpace(headers.Get(name))
	if value == "" {
		return ""
	}
	if strings.EqualFold(name, "Forwarded") {
		match := forwardedForPattern.FindStringSubmatch(value)
		if len(match) != 2 {
			return ""
		}
		value = match[1]
	} else {
		value = strings.TrimSpace(strings.Split(value, ",")[0])
	}
	value = strings.Trim(value, "[]\"")
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	if ip := net.ParseIP(value); ip != nil {
		return ip.String()
	}
	return ""
}

func Middleware(options Options) echo.MiddlewareFunc {
	if options.MaxBodyBytes <= 0 {
		options.MaxBodyBytes = 2 << 20
	}
	if options.MaxUploadBytes < options.MaxBodyBytes {
		options.MaxUploadBytes = options.MaxBodyBytes
	}
	if options.MaxHeaderCount <= 0 {
		options.MaxHeaderCount = 100
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) (err error) {
			started := time.Now()
			request := c.Request()
			requestID := normalizedRequestID(request.Header.Get(RequestIDHeader))
			request.Header.Set(RequestIDHeader, requestID)
			c.Response().Header().Set(RequestIDHeader, requestID)
			setSecurityHeaders(c.Response().Header(), options.HSTS)
			if strings.HasPrefix(request.URL.Path, "/api/") {
				c.Response().Header().Set("Cache-Control", "no-store")
			}

			defer func() {
				if recovered := recover(); recovered != nil {
					slog.Error("panic serving control API request", "request_id", requestID,
						"method", request.Method, "path", request.URL.Path, "panic", recovered,
						"stack", string(debug.Stack()))
					err = echo.NewHTTPError(http.StatusInternalServerError, "internal server error")
				}
				status := 0
				var responseSize int64
				if response, unwrapErr := echo.UnwrapResponse(c.Response()); unwrapErr == nil && response != nil {
					status = response.Status
					responseSize = response.Size
				}
				if status == 0 {
					status = statusFromError(err)
				}
				slog.Info("control API request", "request_id", requestID, "method", request.Method,
					"path", request.URL.Path, "route", c.Path(), "status", status,
					"duration_ms", time.Since(started).Milliseconds(), "bytes", responseSize,
					"remote_ip", c.RealIP(), "user_agent", request.UserAgent())
			}()

			if len(request.Header) > options.MaxHeaderCount {
				return echo.NewHTTPError(http.StatusRequestHeaderFieldsTooLarge, "too many request headers")
			}
			limit := options.MaxBodyBytes
			if strings.HasPrefix(strings.ToLower(request.Header.Get("Content-Type")), "multipart/form-data") {
				limit = options.MaxUploadBytes
			}
			if request.ContentLength > limit {
				return echo.NewHTTPError(http.StatusRequestEntityTooLarge, "request body is too large")
			}
			request.Body = http.MaxBytesReader(c.Response(), request.Body, limit)
			if err := validateBrowserRequest(request, options); err != nil {
				return err
			}
			return next(c)
		}
	}
}

func validateBrowserRequest(request *http.Request, options Options) error {
	if request.Method == http.MethodGet || request.Method == http.MethodHead || request.Method == http.MethodOptions {
		return nil
	}
	if site := strings.ToLower(strings.TrimSpace(request.Header.Get("Sec-Fetch-Site"))); site != "" &&
		site != "same-origin" && site != "same-site" && site != "none" {
		return echo.NewHTTPError(http.StatusForbidden, "cross-site request rejected")
	}
	if origin := strings.TrimSpace(request.Header.Get("Origin")); origin != "" && !sameOrigin(request, origin) {
		return echo.NewHTTPError(http.StatusForbidden, "request origin rejected")
	}
	if options.SessionCookieName == "" || options.CSRFCookieName == "" {
		return nil
	}
	if session, err := request.Cookie(options.SessionCookieName); err != nil || session.Value == "" {
		return nil
	}
	csrfCookie, err := request.Cookie(options.CSRFCookieName)
	csrfHeader := request.Header.Get("X-CSRF-Token")
	if err != nil || csrfCookie.Value == "" || csrfHeader == "" ||
		subtle.ConstantTimeCompare([]byte(csrfCookie.Value), []byte(csrfHeader)) != 1 {
		return echo.NewHTTPError(http.StatusForbidden, "CSRF token is missing or invalid")
	}
	return nil
}

func sameOrigin(request *http.Request, rawOrigin string) bool {
	origin, err := url.Parse(rawOrigin)
	if err != nil || origin.Host == "" {
		return false
	}
	scheme := "http"
	if request.TLS != nil {
		scheme = "https"
	}
	if forwarded := strings.TrimSpace(strings.Split(request.Header.Get("X-Forwarded-Proto"), ",")[0]); forwarded == "http" || forwarded == "https" {
		scheme = forwarded
	}
	return strings.EqualFold(origin.Scheme, scheme) && strings.EqualFold(origin.Host, request.Host)
}

func setSecurityHeaders(header http.Header, hsts bool) {
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
	header.Set("Cross-Origin-Opener-Policy", "same-origin")
	header.Set("Cross-Origin-Resource-Policy", "same-origin")
	header.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; object-src 'none'; form-action 'self'")
	if hsts {
		header.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
	}
}

func normalizedRequestID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return uuid.NewString()
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z') && !(character >= 'A' && character <= 'Z') &&
			!(character >= '0' && character <= '9') && !strings.ContainsRune("._:-", character) {
			return uuid.NewString()
		}
	}
	return value
}

func statusFromError(err error) int {
	if err == nil {
		return http.StatusOK
	}
	var httpError *echo.HTTPError
	if errors.As(err, &httpError) && httpError.Code != 0 {
		return httpError.Code
	}
	return http.StatusInternalServerError
}

type RateLimiter struct {
	redis    rateStore
	local    atomic.Pointer[localRateStore]
	degraded atomic.Bool
}

func NewRateLimiter(client *redis.Client) *RateLimiter {
	limiter := &RateLimiter{}
	if client != nil {
		limiter.redis = client
	}
	limiter.local.Store(newLocalRateStore())
	return limiter
}

type rateStore interface {
	Incr(context.Context, string) *redis.IntCmd
	Expire(context.Context, string, time.Duration) *redis.BoolCmd
}

// localRateStoreMaxKeys caps in-memory buckets during Redis outages so a
// flood of distinct keys cannot grow process memory without bound.
const localRateStoreMaxKeys = 4096

// localRateOverflowKey collapses excess distinct keys into one shared counter
// once the store is at capacity. Attackers pay a shared quota instead of RAM.
const localRateOverflowKey = "__overflow__"

// localRateStore is the in-memory fixed-window fallback used when the Redis
// backend is unavailable. It protects a single control-plane instance only;
// with multiple control-plane replicas each process enforces its own counters,
// so effective limits during a Redis outage scale roughly with replica count.
type localRateStore struct {
	mu      sync.Mutex
	buckets map[string]*localRateBucket
}

type localRateBucket struct {
	count   int64
	expires time.Time
}

func newLocalRateStore() *localRateStore {
	return &localRateStore{buckets: map[string]*localRateBucket{}}
}

func (s *localRateStore) evictExpired(now time.Time) {
	for candidate, bucket := range s.buckets {
		if !now.Before(bucket.expires) {
			delete(s.buckets, candidate)
		}
	}
}

func (s *localRateStore) incr(key string, window time.Duration, now time.Time) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evictExpired(now)
	bucket := s.buckets[key]
	if bucket == nil {
		if len(s.buckets) >= localRateStoreMaxKeys {
			// Prefer collapsing unknown keys over unbounded growth.
			key = localRateOverflowKey
			bucket = s.buckets[key]
		}
		if bucket == nil || !now.Before(bucket.expires) {
			bucket = &localRateBucket{expires: now.Add(window)}
			s.buckets[key] = bucket
		}
	} else if !now.Before(bucket.expires) {
		bucket = &localRateBucket{expires: now.Add(window)}
		s.buckets[key] = bucket
	}
	bucket.count++
	return bucket.count
}

func (l *RateLimiter) Limit(name string, maximum int64, window time.Duration) echo.MiddlewareFunc {
	return l.LimitKeyed(name, maximum, window, func(c *echo.Context) string { return c.RealIP() })
}

// incr counts one hit against the fixed window, preferring the shared Redis
// backend and degrading to the in-memory store when Redis is unavailable so
// endpoints stay reachable during a Redis outage. Multi-replica deployments
// should treat the local fallback as best-effort: each replica has independent
// counters, so auth-sensitive routes are weaker until Redis recovers.
func (l *RateLimiter) incr(ctx context.Context, key string, window time.Duration) int64 {
	if l.redis != nil {
		count, err := l.redis.Incr(ctx, key).Result()
		if err == nil {
			err = l.redis.Expire(ctx, key, window+time.Second).Err()
		}
		if err == nil {
			if l.degraded.CompareAndSwap(true, false) {
				slog.Info("rate limiter recovered to the Redis backend")
			}
			return count
		}
		if l.degraded.CompareAndSwap(false, true) {
			slog.Error("rate limiter degraded to the in-memory backend", "error", err)
		}
	} else if l.degraded.CompareAndSwap(false, true) {
		slog.Error("rate limiter has no Redis backend; using the in-memory backend")
	}
	local := l.local.Load()
	if local == nil {
		// RateLimiter instances built as struct literals lazily get a store.
		l.local.CompareAndSwap(nil, newLocalRateStore())
		local = l.local.Load()
	}
	return local.incr(key, window, time.Now().UTC())
}

// LimitKeyed rate-limits with a caller-provided bucket key, e.g. the
// authenticated user ID for endpoints reachable only with a session.
func (l *RateLimiter) LimitKeyed(name string, maximum int64, window time.Duration, keyFunc func(*echo.Context) string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if l == nil {
				return echo.NewHTTPError(http.StatusServiceUnavailable, "rate limiting unavailable")
			}
			bucket := time.Now().UTC().Unix() / max(int64(window.Seconds()), 1)
			key := fmt.Sprintf("control-api:rate:%s:%s:%d", name, keyFunc(c), bucket)
			count := l.incr(c.Request().Context(), key, window)
			remaining := max(maximum-count, 0)
			c.Response().Header().Set("X-RateLimit-Limit", strconv.FormatInt(maximum, 10))
			c.Response().Header().Set("X-RateLimit-Remaining", strconv.FormatInt(remaining, 10))
			if count > maximum {
				c.Response().Header().Set("Retry-After", strconv.FormatInt(max(int64(window.Seconds()), 1), 10))
				return echo.NewHTTPError(http.StatusTooManyRequests, "too many requests")
			}
			return next(c)
		}
	}
}

func Delay(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
