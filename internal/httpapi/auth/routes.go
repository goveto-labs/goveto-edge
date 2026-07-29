// Package auth registers authentication HTTP endpoints.
package auth

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/audit"
	authn "goveto-edge/internal/auth"
	"goveto-edge/internal/captcha"
	"goveto-edge/internal/httpapi/types"
	"goveto-edge/internal/httpsecurity"
	"goveto-edge/internal/password"
	"goveto-edge/internal/settings"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Code     string `json:"code"`
}

type userResponse struct {
	ID              string           `json:"id"`
	Email           string           `json:"email"`
	Name            string           `json:"name"`
	Role            model.UserRole   `json:"role"`
	Status          model.UserStatus `json:"status"`
	TOTPEnabled     bool             `json:"totp_enabled"`
	TOTPRequired    bool             `json:"totp_required,omitempty"`
	IsInstanceOwner bool             `json:"is_instance_owner"`
}

func newUserResponse(ctx context.Context, settingStore *settings.Store, user *model.User) (userResponse, error) {
	owner, err := settingStore.IsInstanceOwner(ctx, user.Id)
	if err != nil {
		return userResponse{}, err
	}
	return userResponse{
		ID: user.Id, Email: user.Email, Name: user.Name,
		Role: user.Role, Status: user.Status, TOTPEnabled: hasTOTP(user), IsInstanceOwner: owner,
	}, nil
}

func Register(e *echo.Echo, db *client.Client, sessions *authn.SessionStore, settingStore *settings.Store, captchaVerifier *captcha.Verifier, limiter *httpsecurity.RateLimiter) {
	group := e.Group("/api/v1/auth")
	group.POST("/login", login(db, sessions, settingStore), limiter.Limit("login", 10, time.Minute))
	group.POST("/register", register(db, settingStore, captchaVerifier), limiter.Limit("register", 5, time.Hour))
	group.GET("/registration-config", registrationConfig(settingStore), limiter.Limit("captcha-config", 60, time.Minute))
	group.GET("/me", me(db, settingStore), authn.RequireAuth)
	group.POST("/logout", logout(sessions), authn.RequireAuth)
	// Endpoints that verify the current password are throttled per user so a
	// hijacked session cannot brute-force the account password online.
	sensitive := limiter.LimitKeyed("auth-sensitive", 10, time.Minute, rateKeyUser)
	registerTOTP(group, db, sessions, settingStore, sensitive)
	registerSessions(group, db, sessions)
	registerPasswords(group, db, sessions, limiter, sensitive)
}

func rateKeyUser(c *echo.Context) string {
	if uid := authn.CurrentUID(c); uid != "" {
		return "user:" + uid
	}
	return "ip:" + c.RealIP()
}

// @summary Login
// @description Authenticate with email, password and optional TOTP code; sets session cookie.
// @Tags auth
func login(db *client.Client, sessions *authn.SessionStore, settingStore *settings.Store) echo.HandlerFunc {
	dummyHash, _ := password.Hash("invalid-login-password")
	return func(c *echo.Context) error {
		var input loginRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}

		input.Email = strings.ToLower(strings.TrimSpace(input.Email))
		audit.SetActor(c, "", input.Email)
		audit.SetResourceID(c, input.Email)
		if input.Email == "" || input.Password == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "email and password are required")
		}

		user, err := db.User.Query().
			Where(query.User.Email.Equals(input.Email)).
			First(c.Request().Context())
		if err != nil {
			return err
		}
		if user == nil {
			_ = password.Verify(dummyHash, input.Password)
			_ = httpsecurity.Delay(c.Request().Context(), loginFailureDelay(0))
			return echo.NewHTTPError(http.StatusUnauthorized, "invalid email or password")
		}

		locked := user.LockedUntil != nil && time.Now().UTC().Before(*user.LockedUntil)
		passwordOK := password.Verify(user.PasswordHash, input.Password)
		if user.Status != model.UserStatusACTIVE || locked || !passwordOK {
			attempts := user.FailedLoginAttempts
			if !locked && user.Status == model.UserStatusACTIVE {
				if err := recordLoginFailure(c.Request().Context(), db, user.Id); err != nil {
					return err
				}
				attempts++
			}
			_ = httpsecurity.Delay(c.Request().Context(), loginFailureDelay(attempts))
			return echo.NewHTTPError(http.StatusUnauthorized, "invalid email or password")
		}

		if hasTOTP(user) {
			if input.Code == "" {
				// The two-step flow (password first, then the code prompt)
				// lands here on every login; it must not feed the lockout.
				_ = httpsecurity.Delay(c.Request().Context(), loginFailureDelay(user.FailedLoginAttempts))
				return echo.NewHTTPError(http.StatusUnauthorized, "totp code is required")
			}
			valid, verifyErr := verifySecondFactor(c.Request().Context(), db, sessions, user, input.Code)
			if verifyErr != nil {
				return verifyErr
			}
			if !valid {
				if err := recordLoginFailure(c.Request().Context(), db, user.Id); err != nil {
					return err
				}
				_ = httpsecurity.Delay(c.Request().Context(), loginFailureDelay(user.FailedLoginAttempts+1))
				return echo.NewHTTPError(http.StatusUnauthorized, "invalid totp code")
			}
		}

		audit.SetActor(c, user.Id, user.Email)
		totpRequired, err := settingStore.RequireTOTP(c.Request().Context())
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		if _, err = db.User.Update().Where(query.User.Id.Equals(user.Id)).Set(
			query.User.FailedLoginAttempts.Set(0), query.User.LastFailedLoginAt.SetNull(),
			query.User.LockedUntil.SetNull(), query.User.LastLoginAt.Set(now),
		).DoMany(c.Request().Context()); err != nil {
			return err
		}
		token, err := sessions.Create(c.Request().Context(), user.Id, authn.SessionMetadata{
			IPAddress: c.RealIP(), UserAgent: c.Request().UserAgent(),
		})
		if err != nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "session storage unavailable")
		}

		if err := sessions.SetCookie(c, token); err != nil {
			return err
		}
		response, err := newUserResponse(c.Request().Context(), settingStore, user)
		if err != nil {
			return err
		}
		response.TOTPRequired = totpRequired
		audit.SetChange(c, nil, response)
		return types.JSON(c, http.StatusOK, response)
	}
}

// @summary Current user
// @description Return the authenticated user's profile.
// @Tags auth
func me(db *client.Client, settingStore *settings.Store) echo.HandlerFunc {
	return func(c *echo.Context) error {
		user, err := db.User.FindUnique(
			c.Request().Context(),
			query.User.Id.Equals(authn.CurrentUID(c)),
		)
		if err != nil {
			return err
		}
		if user == nil || user.Status != model.UserStatusACTIVE {
			return echo.NewHTTPError(http.StatusUnauthorized, "user is unavailable")
		}
		response, err := newUserResponse(c.Request().Context(), settingStore, user)
		if err != nil {
			return err
		}
		response.TOTPRequired, err = settingStore.RequireTOTP(c.Request().Context())
		if err != nil {
			return err
		}
		return types.JSON(c, http.StatusOK, response)
	}
}

func logout(sessions *authn.SessionStore) echo.HandlerFunc {
	return func(c *echo.Context) error {
		err := sessions.Logout(c)
		if err != nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "session storage unavailable")
		}
		return c.NoContent(http.StatusNoContent)
	}
}

func recordLoginFailure(ctx context.Context, db *client.Client, userID string) error {
	_, err := db.RawExec(ctx, `UPDATE users SET
		failed_login_attempts=CASE WHEN locked_until IS NOT NULL AND locked_until<=NOW()
			THEN 1 ELSE failed_login_attempts+1 END,
		locked_until=CASE WHEN (CASE WHEN locked_until IS NOT NULL AND locked_until<=NOW()
			THEN 1 ELSE failed_login_attempts+1 END)>=5 THEN NOW()+INTERVAL '15 minutes' ELSE NULL END,
		last_failed_login_at=NOW(), updated_at=NOW() WHERE id=$1`, userID)
	return err
}

func loginFailureDelay(attempts int) time.Duration {
	return 500*time.Millisecond + time.Duration(min(attempts, 5))*150*time.Millisecond
}

func hasTOTP(user *model.User) bool {
	return user != nil && user.TotpSecret != nil && strings.TrimSpace(*user.TotpSecret) != ""
}
