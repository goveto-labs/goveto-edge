// Package auth registers authentication HTTP endpoints.
package auth

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"

	"github.com/pquerna/otp/totp"
	authn "goveto-edge/internal/auth"
	"goveto-edge/internal/captcha"
	"goveto-edge/internal/httpapi/types"
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
	ID     string           `json:"id"`
	Email  string           `json:"email"`
	Name   string           `json:"name"`
	Role   model.UserRole   `json:"role"`
	Status model.UserStatus `json:"status"`
}

func newUserResponse(user *model.User) userResponse {
	return userResponse{
		ID: user.Id, Email: user.Email, Name: user.Name,
		Role: user.Role, Status: user.Status,
	}
}

func Register(e *echo.Echo, db *client.Client, sessions *authn.SessionStore, settingStore *settings.Store, captchaVerifier *captcha.Verifier) {
	group := e.Group("/api/v1/auth")
	group.POST("/login", login(db, sessions))
	group.POST("/register", register(db, settingStore, captchaVerifier))
	group.GET("/registration-config", registrationConfig(settingStore))
	group.GET("/me", me(db), authn.RequireAuth)
}

// @summary Login
// @description Authenticate with email, password and optional TOTP code; sets session cookie.
// @Tags auth
func login(db *client.Client, sessions *authn.SessionStore) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input loginRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}

		input.Email = strings.ToLower(strings.TrimSpace(input.Email))
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
			return echo.NewHTTPError(http.StatusUnauthorized, "invalid email or password")
		}

		if user.Status != model.UserStatusACTIVE || !password.Verify(user.PasswordHash, input.Password) {
			return echo.NewHTTPError(http.StatusUnauthorized, "invalid email or password")
		}

		if user.TotpSecret != nil && strings.TrimSpace(*user.TotpSecret) != "" {
			if input.Code == "" {
				return echo.NewHTTPError(http.StatusUnauthorized, "totp code is required")
			}
			if !totp.Validate(strings.TrimSpace(input.Code), strings.TrimSpace(*user.TotpSecret)) {
				return echo.NewHTTPError(http.StatusUnauthorized, "invalid totp code")
			}
		}

		token, err := sessions.Create(c.Request().Context(), user.Id)
		if err != nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "session storage unavailable")
		}

		sessions.SetCookie(c, token)
		return types.JSON(c, http.StatusOK, newUserResponse(user))
	}
}

// @summary Current user
// @description Return the authenticated user's profile.
// @Tags auth
func me(db *client.Client) echo.HandlerFunc {
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
		return types.JSON(c, http.StatusOK, newUserResponse(user))
	}
}
