// Package auth implements control-plane session authentication.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/redis/go-redis/v9"

	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

const currentUIDKey = "auth.current_uid"
const currentSessionTokenKey = "auth.current_session_token"

type currentUserContextKey struct{}

type SessionStore struct {
	redis      redisStore
	cookieName string
	ttl        time.Duration
	secure     bool
}

type redisStore interface {
	Get(ctx context.Context, key string) *redis.StringCmd
	Set(ctx context.Context, key string, value any, expiration time.Duration) *redis.StatusCmd
	Del(ctx context.Context, keys ...string) *redis.IntCmd
}

func NewSessionStore(client *redis.Client, cookieName string, ttl time.Duration, secure bool) *SessionStore {
	return &SessionStore{redis: client, cookieName: cookieName, ttl: ttl, secure: secure}
}

func (s *SessionStore) Create(ctx context.Context, uid string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	if err := s.redis.Set(ctx, sessionKey(token), uid, s.ttl).Err(); err != nil {
		return "", err
	}
	return token, nil
}

func (s *SessionStore) SetCookie(c *echo.Context, token string) {
	c.SetCookie(&http.Cookie{
		Name:     s.cookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(s.ttl.Seconds()),
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *SessionStore) clearCookie(c *echo.Context) {
	c.SetCookie(&http.Cookie{
		Name:     s.cookieName,
		Path:     "/",
		MaxAge:   -1,
		Expires:  time.Unix(1, 0),
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// Session loads a valid UID into the Echo context when a session exists.
func (s *SessionStore) Session(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		cookie, err := c.Cookie(s.cookieName)
		if err == nil && cookie.Value != "" {
			uid, getErr := s.redis.Get(c.Request().Context(), sessionKey(cookie.Value)).Result()
			if getErr == nil {
				c.Set(currentUIDKey, uid)
				c.Set(currentSessionTokenKey, cookie.Value)
			} else if !errors.Is(getErr, redis.Nil) {
				return echo.NewHTTPError(http.StatusServiceUnavailable, "session storage unavailable")
			}
		}
		return next(c)
	}
}

// RequireActiveUser invalidates sessions as soon as their user is disabled or
// deleted. It is intentionally evaluated on every authenticated request.
func (s *SessionStore) RequireActiveUser(db *client.Client) echo.MiddlewareFunc {
	return s.requireActiveUser(func(ctx context.Context, uid string) (*model.User, error) {
		return db.User.FindUnique(ctx, query.User.Id.Equals(uid))
	})
}

type activeUserLoader func(context.Context, string) (*model.User, error)

func (s *SessionStore) requireActiveUser(load activeUserLoader) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			uid := CurrentUID(c)
			if uid == "" {
				return next(c)
			}
			user, err := load(c.Request().Context(), uid)
			if err != nil {
				return err
			}
			if user != nil && user.Status == model.UserStatusACTIVE {
				ctx := context.WithValue(c.Request().Context(), currentUserContextKey{}, user)
				c.SetRequest(c.Request().WithContext(ctx))
				return next(c)
			}
			if err := s.invalidate(c); err != nil {
				return echo.NewHTTPError(http.StatusServiceUnavailable, "session storage unavailable")
			}
			return next(c)
		}
	}
}

func (s *SessionStore) invalidate(c *echo.Context) error {
	token, _ := c.Get(currentSessionTokenKey).(string)
	c.Set(currentUIDKey, "")
	c.Set(currentSessionTokenKey, "")
	s.clearCookie(c)
	if token == "" {
		return nil
	}
	return s.redis.Del(c.Request().Context(), sessionKey(token), selectedClusterKey(token)).Err()
}

func (s *SessionStore) SelectedCluster(ctx context.Context, c *echo.Context) (string, error) {
	token, _ := c.Get(currentSessionTokenKey).(string)
	if token == "" {
		return "", nil
	}
	value, err := s.redis.Get(ctx, selectedClusterKey(token)).Result()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	return value, err
}

func (s *SessionStore) SetSelectedCluster(ctx context.Context, c *echo.Context, clusterID string) error {
	token, _ := c.Get(currentSessionTokenKey).(string)
	if token == "" {
		return errors.New("session token unavailable")
	}
	return s.redis.Set(ctx, selectedClusterKey(token), clusterID, s.ttl).Err()
}

// RequireAuth rejects requests without a UID loaded by Session.
func RequireAuth(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if CurrentUID(c) == "" {
			return echo.NewHTTPError(http.StatusUnauthorized, "authentication required")
		}
		return next(c)
	}
}

func CurrentUID(c *echo.Context) string {
	uid, _ := c.Get(currentUIDKey).(string)
	return uid
}

// CurrentUser returns the active user loaded by RequireActiveUser. The uid
// check prevents callers from reusing a cached principal for another subject.
func CurrentUser(ctx context.Context, uid string) (*model.User, bool) {
	user, ok := ctx.Value(currentUserContextKey{}).(*model.User)
	return user, ok && user != nil && user.Id == uid
}

func sessionKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "session:" + hex.EncodeToString(sum[:])
}
func selectedClusterKey(token string) string { return sessionKey(token) + ":cluster" }
