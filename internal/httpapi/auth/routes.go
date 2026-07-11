// Package auth registers authentication HTTP endpoints.
package auth

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/pquerna/otp/totp"
	authn "goveto-edge/internal/auth"
	"goveto-edge/internal/captcha"
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

func Register(e *echo.Echo, db *client.Client, sessions *authn.SessionStore, settingStore *settings.Store, captchaVerifier *captcha.Verifier) {
	group := e.Group("/api/v1/auth")
	group.POST("/login", login(db, sessions))
	group.POST("/register", register(db, settingStore, captchaVerifier))
	group.GET("/registration-config", registrationConfig(settingStore))
	group.GET("/me", me, authn.RequireAuth)
}

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

		user, err := db.User.Query().Where(query.User.Email.Equals(input.Email)).First(c.Request().Context())
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return echo.NewHTTPError(http.StatusUnauthorized, "invalid email or password")
			}
			return err
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
		return c.JSON(http.StatusOK, userResponse{ID: user.Id, Email: user.Email, Name: user.Name, Role: user.Role, Status: user.Status})
	}
}

func me(c *echo.Context) error {
	return c.JSON(http.StatusOK, map[string]string{"uid": authn.CurrentUID(c)})
}
