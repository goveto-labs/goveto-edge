// Package auth implements control-plane session authentication.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/redis/go-redis/v9"

	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

const currentUIDKey = "auth.current_uid"
const currentSessionTokenKey = "auth.current_session_token"
const currentSessionIDKey = "auth.current_session_id"

var ErrSessionNotFound = errors.New("session not found")

var ErrExternalAuthStateNotFound = errors.New("external authentication state not found")

type currentUserContextKey struct{}

type SessionStore struct {
	redis      redisStore
	db         *client.Client
	cookieName string
	csrfCookie string
	ttl        time.Duration
	secure     bool
}

type redisStore interface {
	Get(ctx context.Context, key string) *redis.StringCmd
	GetDel(ctx context.Context, key string) *redis.StringCmd
	Set(ctx context.Context, key string, value any, expiration time.Duration) *redis.StatusCmd
	SetNX(ctx context.Context, key string, value any, expiration time.Duration) *redis.BoolCmd
	Del(ctx context.Context, keys ...string) *redis.IntCmd
}

func NewSessionStore(redisClient *redis.Client, db *client.Client, cookieName string, ttl time.Duration, secure bool) *SessionStore {
	return &SessionStore{redis: redisClient, db: db, cookieName: cookieName, csrfCookie: cookieName + "_csrf", ttl: ttl, secure: secure}
}

type SessionMetadata struct {
	IPAddress string
	UserAgent string
}

type ExternalAuthState struct {
	CodeVerifier string `json:"code_verifier"`
	ReturnPath   string `json:"return_path"`
	ProviderID   string `json:"provider_id"`
}

func (s *SessionStore) StoreExternalAuthState(ctx context.Context, state string, value ExternalAuthState) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.redis.Set(ctx, "external-auth:state:"+state, encoded, 10*time.Minute).Err()
}

func (s *SessionStore) ConsumeExternalAuthState(ctx context.Context, state string) (ExternalAuthState, error) {
	key := "external-auth:state:" + state
	encoded, err := s.redis.GetDel(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return ExternalAuthState{}, ErrExternalAuthStateNotFound
	}
	if err != nil {
		return ExternalAuthState{}, err
	}
	var value ExternalAuthState
	if err = json.Unmarshal(encoded, &value); err != nil {
		return ExternalAuthState{}, err
	}
	return value, nil
}

func (s *SessionStore) Create(ctx context.Context, uid string, metadata ...SessionMetadata) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	hash := tokenHash(token)
	if s.db != nil {
		sets := []query.UserSessionSetClause{
			query.UserSession.UserId.Set(uid), query.UserSession.TokenHash.Set(hash),
			query.UserSession.ExpiresAt.Set(time.Now().UTC().Add(s.ttl)),
			query.UserSession.LastSeenAt.Set(time.Now().UTC()),
		}
		if len(metadata) > 0 {
			if value := strings.TrimSpace(metadata[0].IPAddress); value != "" {
				sets = append(sets, query.UserSession.IpAddress.Set(value))
			}
			if value := strings.TrimSpace(metadata[0].UserAgent); value != "" {
				sets = append(sets, query.UserSession.UserAgent.Set(value))
			}
		}
		if _, err := s.db.UserSession.Create().Set(sets...).Do(ctx); err != nil {
			return "", err
		}
	}
	if err := s.redis.Set(ctx, sessionKey(token), uid, s.ttl).Err(); err != nil {
		if s.db != nil {
			_, _ = s.db.UserSession.Delete().Where(query.UserSession.TokenHash.Equals(hash)).DoMany(context.WithoutCancel(ctx))
		}
		return "", err
	}
	return token, nil
}

func (s *SessionStore) SetCookie(c *echo.Context, token string) error {
	c.SetCookie(&http.Cookie{
		Name:     s.cookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(s.ttl.Seconds()),
		Expires:  time.Now().Add(s.ttl),
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteLaxMode,
	})
	return s.setCSRFCookie(c)
}

func (s *SessionStore) setCSRFCookie(c *echo.Context) error {
	csrfRaw := make([]byte, 32)
	if _, err := rand.Read(csrfRaw); err != nil {
		return err
	}
	c.SetCookie(&http.Cookie{
		Name: s.csrfCookie, Value: base64.RawURLEncoding.EncodeToString(csrfRaw), Path: "/",
		MaxAge: int(s.ttl.Seconds()), Expires: time.Now().Add(s.ttl), HttpOnly: false, Secure: s.secure,
		SameSite: http.SameSiteLaxMode,
	})
	return nil
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
	if s.csrfCookie != "" {
		c.SetCookie(&http.Cookie{
			Name: s.csrfCookie, Path: "/", MaxAge: -1, Expires: time.Unix(1, 0),
			HttpOnly: false, Secure: s.secure, SameSite: http.SameSiteLaxMode,
		})
	}
}

// Session loads a valid UID into the Echo context when a session exists.
func (s *SessionStore) Session(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		cookie, err := c.Cookie(s.cookieName)
		if err == nil && cookie.Value != "" {
			uid, getErr := s.redis.Get(c.Request().Context(), sessionKey(cookie.Value)).Result()
			if getErr == nil {
				valid, sessionID, validationErr := s.validate(c.Request().Context(), uid, cookie.Value)
				if validationErr != nil {
					return echo.NewHTTPError(http.StatusServiceUnavailable, "session storage unavailable")
				}
				if valid {
					c.Set(currentUIDKey, uid)
					c.Set(currentSessionTokenKey, cookie.Value)
					c.Set(currentSessionIDKey, sessionID)
					// Reissue the CSRF cookie when it went missing, so a valid
					// session is never permanently locked out of mutations.
					if _, csrfErr := c.Cookie(s.csrfCookie); csrfErr != nil {
						if err := s.setCSRFCookie(c); err != nil {
							return err
						}
					}
				} else {
					_ = s.redis.Del(c.Request().Context(), sessionKey(cookie.Value), selectedClusterKey(cookie.Value)).Err()
					s.clearCookie(c)
				}
			} else if errors.Is(getErr, redis.Nil) {
				s.clearCookie(c)
			} else {
				return echo.NewHTTPError(http.StatusServiceUnavailable, "session storage unavailable")
			}
		}
		return next(c)
	}
}

func (s *SessionStore) validate(ctx context.Context, uid, token string) (bool, string, error) {
	if s.db == nil {
		return true, "", nil
	}
	session, err := s.db.UserSession.FindUnique(ctx, query.UserSession.TokenHash.Equals(tokenHash(token)))
	if err != nil {
		return false, "", err
	}
	if session == nil || session.UserId != uid || session.RevokedAt != nil || !time.Now().UTC().Before(session.ExpiresAt) {
		return false, "", nil
	}
	if time.Since(session.LastSeenAt) >= 5*time.Minute {
		_, _ = s.db.UserSession.Update().Where(query.UserSession.Id.Equals(session.Id)).Set(
			query.UserSession.LastSeenAt.Set(time.Now().UTC()),
		).DoMany(ctx)
	}
	return true, session.Id, nil
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
	c.Set(currentSessionIDKey, "")
	s.clearCookie(c)
	if token == "" {
		return nil
	}
	var databaseErr error
	if s.db != nil {
		_, databaseErr = s.db.UserSession.Update().Where(query.UserSession.TokenHash.Equals(tokenHash(token))).Set(
			query.UserSession.RevokedAt.Set(time.Now().UTC()),
		).DoMany(c.Request().Context())
	}
	return errors.Join(databaseErr, s.redis.Del(c.Request().Context(), sessionKey(token), selectedClusterKey(token)).Err())
}

func (s *SessionStore) Logout(c *echo.Context) error { return s.invalidate(c) }

func (s *SessionStore) Revoke(ctx context.Context, uid, id string) error {
	if s.db == nil {
		return errors.New("session database unavailable")
	}
	rows, err := client.Raw[struct {
		TokenHash string `db:"token_hash"`
	}](ctx, s.db, `UPDATE user_sessions SET revoked_at=NOW()
		WHERE id=$1 AND user_id=$2 AND revoked_at IS NULL RETURNING token_hash`, id, uid)
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return ErrSessionNotFound
	}
	return s.redis.Del(ctx, sessionKeyForHash(rows[0].TokenHash), sessionKeyForHash(rows[0].TokenHash)+":cluster").Err()
}

func (s *SessionStore) RevokeAll(ctx context.Context, uid, exceptID string) error {
	if s.db == nil {
		return errors.New("session database unavailable")
	}
	rows, err := client.Raw[struct {
		TokenHash string `db:"token_hash"`
	}](ctx, s.db, `UPDATE user_sessions SET revoked_at=NOW()
		WHERE user_id=$1 AND revoked_at IS NULL AND ($2='' OR id<>$2) RETURNING token_hash`, uid, exceptID)
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, sessionKeyForHash(row.TokenHash), sessionKeyForHash(row.TokenHash)+":cluster")
	}
	if len(keys) == 0 {
		return nil
	}
	return s.redis.Del(ctx, keys...).Err()
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

func CurrentSessionID(c *echo.Context) string {
	id, _ := c.Get(currentSessionIDKey).(string)
	return id
}

func (s *SessionStore) CookieName() string     { return s.cookieName }
func (s *SessionStore) CSRFCookieName() string { return s.csrfCookie }

// CurrentUser returns the active user loaded by RequireActiveUser. The uid
// check prevents callers from reusing a cached principal for another subject.
func CurrentUser(ctx context.Context, uid string) (*model.User, bool) {
	user, ok := ctx.Value(currentUserContextKey{}).(*model.User)
	return user, ok && user != nil && user.Id == uid
}

// ConsumeTOTPCode marks a TOTP code as used for the given user and reports
// whether this was its first use, preventing replay within the code's
// validity window (30s period with +/- one period of allowed skew).
func (s *SessionStore) ConsumeTOTPCode(ctx context.Context, uid, code string) (bool, error) {
	sum := sha256.Sum256([]byte(uid + ":" + strings.TrimSpace(code)))
	return s.redis.SetNX(ctx, "totp:used:"+hex.EncodeToString(sum[:]), "1", 2*time.Minute).Result()
}

// tokenHash is the value persisted in user_sessions.token_hash; Redis keys are
// derived from it via sessionKeyForHash so revocation can go from row to key.
func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
func sessionKeyForHash(hash string) string   { return "session:" + hash }
func sessionKey(token string) string         { return sessionKeyForHash(tokenHash(token)) }
func selectedClusterKey(token string) string { return sessionKey(token) + ":cluster" }
