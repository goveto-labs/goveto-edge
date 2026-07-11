// Package httpapi assembles all control-plane HTTP API modules.
package httpapi

import (
	"database/sql"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/auth"
	"goveto-edge/internal/captcha"
	authapi "goveto-edge/internal/httpapi/auth"
	"goveto-edge/internal/httpapi/health"
	"goveto-edge/internal/settings"
	"goveto-edge/internal/storage/gen/client"
)

func New(db *sql.DB, orm *client.Client, sessions *auth.SessionStore) *echo.Echo {
	e := echo.New()
	e.Use(sessions.Session)
	settingStore := settings.New(orm)
	captchaVerifier := captcha.New()

	health.Register(e, db)
	authapi.Register(e, orm, sessions, settingStore, captchaVerifier)

	return e
}
