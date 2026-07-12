package auth

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/captcha"
	"goveto-edge/internal/password"
	"goveto-edge/internal/settings"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

type registerRequest struct {
	Email        string `json:"email"`
	Password     string `json:"password"`
	Name         string `json:"name"`
	CaptchaToken string `json:"captcha_token"`
}

// @summary Register
// @description Create a new user account when registration is enabled; requires captcha.
// @Tags auth
func register(db *client.Client, settingStore *settings.Store, verifier *captcha.Verifier) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := c.Request().Context()
		enabled, err := settingStore.RegistrationEnabled(ctx)
		if err != nil {
			return err
		}
		if !enabled {
			return echo.NewHTTPError(http.StatusForbidden, "registration is disabled")
		}

		var input registerRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		input.Email = strings.ToLower(strings.TrimSpace(input.Email))
		input.Name = strings.TrimSpace(input.Name)
		if input.Email == "" || input.Name == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "email and name are required")
		}
		if err := password.Validate(input.Password); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}

		captchaConfig, found, err := settingStore.Captcha(ctx)
		if err != nil {
			return err
		}
		if !found {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "captcha is not configured")
		}
		valid, err := verifier.Verify(ctx, captchaConfig.Provider, captchaConfig.SecretKey, input.CaptchaToken, c.RealIP())
		if err != nil {
			return echo.NewHTTPError(http.StatusBadGateway, "captcha verification failed")
		}
		if !valid {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid captcha")
		}

		_, err = db.User.FindUnique(ctx, query.User.Email.Equals(input.Email))
		if err == nil {
			return echo.NewHTTPError(http.StatusConflict, "email is already registered")
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		hash, err := password.Hash(input.Password)
		if err != nil {
			return err
		}
		user, err := db.User.Create().Set(
			query.User.Email.Set(input.Email),
			query.User.PasswordHash.Set(hash),
			query.User.Name.Set(input.Name),
			query.User.Role.Set(model.UserRoleVIEWER),
			query.User.Status.Set(model.UserStatusACTIVE),
		).Do(ctx)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusCreated, userResponse{ID: user.Id, Email: user.Email, Name: user.Name, Role: user.Role, Status: user.Status})
	}
}

// @summary Registration config
// @description Return whether registration is enabled and public captcha settings.
// @Tags auth
func registrationConfig(settingStore *settings.Store) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := c.Request().Context()
		enabled, err := settingStore.RegistrationEnabled(ctx)
		if err != nil {
			return err
		}
		config, found, err := settingStore.Captcha(ctx)
		if err != nil {
			return err
		}
		response := map[string]any{"enabled": enabled}
		if found {
			response["captcha"] = map[string]string{"provider": config.Provider, "site_key": config.SiteKey}
		}
		return c.JSON(http.StatusOK, response)
	}
}
