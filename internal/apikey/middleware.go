package apikey

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/auth"
	"goveto-edge/internal/httpapi/types"
	"goveto-edge/internal/httpsecurity"
	"goveto-edge/internal/storage/gen/model"
)

const (
	// RequestsPerMinute bounds authenticated automation traffic per key.
	RequestsPerMinute = 600
	// invalidAttemptsPerMinute throttles token guessing per source IP; it
	// only applies to unknown tokens, never to valid keys.
	invalidAttemptsPerMinute = 60
	authFailureAuditTimeout  = 3 * time.Second
)

var authFailureAuditSlots = make(chan struct{}, 16)

// AuthFailureEvent is the secret-free authentication failure payload exposed
// to the control-plane audit adapter.
type AuthFailureEvent struct {
	Prefix      string
	SourceIP    string
	UserAgent   string
	RequestID   string
	FailureCode string
}

// AuthFailureRecorder persists a failed API key authentication attempt.
type AuthFailureRecorder func(context.Context, AuthFailureEvent) error

type tokenVerifier func(context.Context, string) (*model.ClusterApiKey, error)
type usageRecorder func(string, string)

// ExtractToken reads the credential from the Authorization bearer header or
// the X-API-Key header. Foreign bearer tokens (anything without the gve1_
// prefix) are ignored rather than rejected so future schemes can coexist.
func ExtractToken(request *http.Request) string {
	if header := strings.TrimSpace(request.Header.Get("Authorization")); header != "" {
		parts := strings.SplitN(header, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			token := strings.TrimSpace(parts[1])
			if strings.HasPrefix(token, TokenPrefix) {
				return token
			}
		}
	}
	if token := strings.TrimSpace(request.Header.Get("X-API-Key")); strings.HasPrefix(token, TokenPrefix) {
		return token
	}
	return ""
}

// Middleware authenticates requests carrying a cluster API key. An explicit
// API key credential takes precedence over cookies so stale or disabled user
// sessions cannot suppress automation authentication.
func (s *Service) Middleware(limiter *httpsecurity.RateLimiter, recorders ...AuthFailureRecorder) echo.MiddlewareFunc {
	return authenticationMiddleware(s.Verify, s.Touch, limiter, recorders...)
}

func authenticationMiddleware(verify tokenVerifier, touch usageRecorder, limiter *httpsecurity.RateLimiter, recorders ...AuthFailureRecorder) echo.MiddlewareFunc {
	var recorder AuthFailureRecorder
	if len(recorders) > 0 {
		recorder = recorders[0]
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			token := ExtractToken(c.Request())
			if token == "" {
				return next(c)
			}
			ctx := c.Request().Context()
			key, err := verify(ctx, token)
			if err != nil {
				code, message, credentialFailure := failureCode(err)
				if !credentialFailure {
					return fmt.Errorf("verify api key: %w", err)
				}
				if errors.Is(err, ErrInvalid) && limiter != nil &&
					!limiter.Take(ctx, "api-key-invalid", c.RealIP(), invalidAttemptsPerMinute, time.Minute) {
					return tooManyRequests(c, time.Minute)
				}
				recordAuthFailure(recorder, AuthFailureEvent{
					Prefix: tokenDisplayPrefix(token), SourceIP: c.RealIP(), UserAgent: c.Request().UserAgent(),
					RequestID: c.Request().Header.Get(httpsecurity.RequestIDHeader), FailureCode: code,
				})
				return types.Error(http.StatusUnauthorized, code, message)
			}
			if limiter != nil && !limiter.Take(ctx, "api-key", key.Id, RequestsPerMinute, time.Minute) {
				return tooManyRequests(c, time.Minute)
			}
			touch(key.Id, c.RealIP())
			auth.ClearCurrentSessionPrincipal(c)
			auth.SetCurrentAPIKey(c, &auth.APIKeyPrincipal{
				KeyID:       key.Id,
				ClusterID:   key.ClusterId,
				Prefix:      key.Prefix,
				CreatedBy:   key.CreatedBy,
				Permissions: PermissionsOf(key),
			})
			return next(c)
		}
	}
}

func tokenDisplayPrefix(token string) string {
	if len(token) >= prefixLength {
		return token[:prefixLength]
	}
	return TokenPrefix
}

func recordAuthFailure(recorder AuthFailureRecorder, event AuthFailureEvent) {
	if recorder == nil {
		return
	}
	select {
	case authFailureAuditSlots <- struct{}{}:
		go func() {
			defer func() { <-authFailureAuditSlots }()
			ctx, cancel := context.WithTimeout(context.Background(), authFailureAuditTimeout)
			defer cancel()
			if err := recorder(ctx, event); err != nil {
				slog.Error("write api key authentication failure audit", "code", event.FailureCode, "error", err)
			}
		}()
	default:
		slog.Warn("api key authentication failure audit queue is full", "code", event.FailureCode)
	}
}

func failureCode(err error) (code, message string, credentialFailure bool) {
	switch {
	case errors.Is(err, ErrExpired):
		return "api_key_expired", "api key has expired", true
	case errors.Is(err, ErrRevoked):
		return "api_key_revoked", "api key has been revoked", true
	case errors.Is(err, ErrDisabled):
		return "api_key_disabled", "api key is disabled", true
	case errors.Is(err, ErrCreatorInactive):
		return "api_key_creator_inactive", "api key creator is inactive", true
	case errors.Is(err, ErrInvalid):
		return "api_key_invalid", "api key is invalid", true
	}
	return "", "", false
}

func tooManyRequests(c *echo.Context, window time.Duration) error {
	c.Response().Header().Set("Retry-After", strconv.FormatInt(max(int64(window.Seconds()), 1), 10))
	return types.Error(http.StatusTooManyRequests, "too_many_requests", "api key rate limit exceeded")
}
