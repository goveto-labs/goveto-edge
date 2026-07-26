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
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"github.com/redis/go-redis/v9"
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

// TrustedProxyIPExtractor returns an X-Forwarded-For based client IP extractor
// that trusts only the given proxy CIDRs, or nil when none are configured.
// Without it, rate limits key on the proxy's address and become global.
func TrustedProxyIPExtractor(trustedCIDRs []string) (echo.IPExtractor, error) {
	if len(trustedCIDRs) == 0 {
		return nil, nil
	}
	options := []echo.TrustOption{
		echo.TrustLoopback(false), echo.TrustLinkLocal(false), echo.TrustPrivateNet(false),
	}
	for _, cidr := range trustedCIDRs {
		_, network, err := net.ParseCIDR(strings.TrimSpace(cidr))
		if err != nil {
			return nil, fmt.Errorf("invalid trusted proxy CIDR %q: %w", cidr, err)
		}
		options = append(options, echo.TrustIPRange(network))
	}
	return echo.ExtractIPFromXFFHeader(options...), nil
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
	redis rateStore
}

func NewRateLimiter(client *redis.Client) *RateLimiter { return &RateLimiter{redis: client} }

type rateStore interface {
	Incr(context.Context, string) *redis.IntCmd
	Expire(context.Context, string, time.Duration) *redis.BoolCmd
}

func (l *RateLimiter) Limit(name string, maximum int64, window time.Duration) echo.MiddlewareFunc {
	return l.LimitKeyed(name, maximum, window, func(c *echo.Context) string { return c.RealIP() })
}

// LimitKeyed rate-limits with a caller-provided bucket key, e.g. the
// authenticated user ID for endpoints reachable only with a session.
func (l *RateLimiter) LimitKeyed(name string, maximum int64, window time.Duration, keyFunc func(*echo.Context) string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if l == nil || l.redis == nil {
				return echo.NewHTTPError(http.StatusServiceUnavailable, "rate limiting unavailable")
			}
			bucket := time.Now().UTC().Unix() / max(int64(window.Seconds()), 1)
			key := fmt.Sprintf("control-api:rate:%s:%s:%d", name, keyFunc(c), bucket)
			count, err := l.redis.Incr(c.Request().Context(), key).Result()
			if err != nil {
				return echo.NewHTTPError(http.StatusServiceUnavailable, "rate limiting unavailable")
			}
			if err := l.redis.Expire(c.Request().Context(), key, window+time.Second).Err(); err != nil {
				return echo.NewHTTPError(http.StatusServiceUnavailable, "rate limiting unavailable")
			}
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
