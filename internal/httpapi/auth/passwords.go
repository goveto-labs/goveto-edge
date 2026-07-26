package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/audit"
	authn "goveto-edge/internal/auth"
	"goveto-edge/internal/httpapi/types"
	"goveto-edge/internal/httpsecurity"
	"goveto-edge/internal/password"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

const passwordResetTTL = 30 * time.Minute

var errInvalidPasswordResetToken = errors.New("invalid or expired password reset token")

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

type issuePasswordResetRequest struct {
	Email string `json:"email"`
}

type completePasswordResetRequest struct {
	Token       string `json:"token"`
	NewPassword string `json:"new_password"`
}

type passwordResetTokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

func registerPasswords(group *echo.Group, db *client.Client, sessions *authn.SessionStore, limiter *httpsecurity.RateLimiter, sensitive echo.MiddlewareFunc) {
	group.PUT("/password", changePassword(db, sessions), authn.RequireAuth, sensitive)
	group.POST("/password-reset/admin-token", issuePasswordReset(db), authn.RequireAuth)
	group.POST("/password-reset", completePasswordReset(db, sessions), limiter.Limit("password-reset", 5, time.Hour))
}

func changePassword(db *client.Client, sessions *authn.SessionStore) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input changePasswordRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		user, err := verifyCurrentPassword(c, db, input.CurrentPassword)
		if err != nil {
			return err
		}
		if err := password.Validate(input.NewPassword); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		hash, err := password.Hash(input.NewPassword)
		if err != nil {
			return err
		}
		if _, err = db.User.Update().Where(query.User.Id.Equals(user.Id)).Set(
			query.User.PasswordHash.Set(hash), query.User.PasswordChangedAt.Set(time.Now().UTC()),
			query.User.FailedLoginAttempts.Set(0), query.User.LockedUntil.SetNull(),
		).DoMany(c.Request().Context()); err != nil {
			return err
		}
		if err = sessions.RevokeAll(c.Request().Context(), user.Id, authn.CurrentSessionID(c)); err != nil {
			return err
		}
		audit.SetChange(c, map[string]any{"password_changed": false}, map[string]any{"password_changed": true})
		return c.NoContent(http.StatusNoContent)
	}
}

func issuePasswordReset(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		actor, ok := authn.CurrentUser(c.Request().Context(), authn.CurrentUID(c))
		if !ok || actor.Role != model.UserRoleADMIN {
			return echo.NewHTTPError(http.StatusForbidden, "administrator role required")
		}
		var input issuePasswordResetRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		email := strings.ToLower(strings.TrimSpace(input.Email))
		user, err := db.User.FindUnique(c.Request().Context(), query.User.Email.Equals(email))
		if err != nil {
			return err
		}
		if user == nil {
			return echo.NewHTTPError(http.StatusNotFound, "user not found")
		}
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return err
		}
		token := base64.RawURLEncoding.EncodeToString(raw)
		expiresAt := time.Now().UTC().Add(passwordResetTTL)
		err = db.Tx(c.Request().Context(), func(tx *client.Client) error {
			if _, err := tx.RawExec(c.Request().Context(), `UPDATE password_reset_tokens SET used_at=NOW()
				WHERE user_id=$1 AND used_at IS NULL`, user.Id); err != nil {
				return err
			}
			_, err := tx.PasswordResetToken.Create().Set(
				query.PasswordResetToken.UserId.Set(user.Id),
				query.PasswordResetToken.TokenHash.Set(passwordResetHash(token)),
				query.PasswordResetToken.ExpiresAt.Set(expiresAt),
			).Do(c.Request().Context())
			return err
		})
		if err != nil {
			return err
		}
		audit.SetResourceID(c, user.Id)
		audit.SetChange(c, nil, map[string]any{"expires_at": expiresAt})
		return types.JSON(c, http.StatusCreated, passwordResetTokenResponse{Token: token, ExpiresAt: expiresAt})
	}
}

func completePasswordReset(db *client.Client, sessions *authn.SessionStore) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input completePasswordResetRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		if err := password.Validate(input.NewPassword); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		hash, err := password.Hash(input.NewPassword)
		if err != nil {
			return err
		}
		userID, err := consumePasswordReset(c.Request().Context(), db, input.Token, hash)
		if errors.Is(err, errInvalidPasswordResetToken) {
			return echo.NewHTTPError(http.StatusBadRequest, "password reset token is invalid or expired")
		}
		if err != nil {
			return err
		}
		if err := sessions.RevokeAll(c.Request().Context(), userID, ""); err != nil {
			return err
		}
		audit.SetResourceID(c, userID)
		return c.NoContent(http.StatusNoContent)
	}
}

func consumePasswordReset(ctx context.Context, db *client.Client, token, newPasswordHash string) (string, error) {
	type resetRow struct {
		ID     string `db:"id"`
		UserID string `db:"user_id"`
	}
	var userID string
	err := db.Tx(ctx, func(tx *client.Client) error {
		rows, err := client.Raw[resetRow](ctx, tx, `SELECT id, user_id FROM password_reset_tokens
			WHERE token_hash=$1 AND used_at IS NULL AND expires_at>NOW() FOR UPDATE`, passwordResetHash(token))
		if err != nil || len(rows) != 1 {
			if err != nil {
				return err
			}
			return errInvalidPasswordResetToken
		}
		userID = rows[0].UserID
		if _, err = tx.User.Update().Where(query.User.Id.Equals(userID)).Set(
			query.User.PasswordHash.Set(newPasswordHash), query.User.PasswordChangedAt.Set(time.Now().UTC()),
			query.User.FailedLoginAttempts.Set(0), query.User.LastFailedLoginAt.SetNull(),
			query.User.LockedUntil.SetNull(),
		).DoMany(ctx); err != nil {
			return err
		}
		_, err = tx.PasswordResetToken.Update().Where(query.PasswordResetToken.Id.Equals(rows[0].ID)).Set(
			query.PasswordResetToken.UsedAt.Set(time.Now().UTC()),
		).DoMany(ctx)
		return err
	})
	return userID, err
}

func passwordResetHash(token string) string {
	hash := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(hash[:])
}
