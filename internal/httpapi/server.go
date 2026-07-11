// Package httpapi assembles all control-plane HTTP API modules.
package httpapi

import (
	"database/sql"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/redis/go-redis/v9"

	"goveto-edge/internal/auth"
	"goveto-edge/internal/captcha"
	authapi "goveto-edge/internal/httpapi/auth"
	"goveto-edge/internal/httpapi/certificates"
	"goveto-edge/internal/httpapi/clusters"
	"goveto-edge/internal/httpapi/health"
	"goveto-edge/internal/httpapi/nodes"
	publishapi "goveto-edge/internal/httpapi/publish"
	purgeapi "goveto-edge/internal/httpapi/purge"
	"goveto-edge/internal/httpapi/sites"
	"goveto-edge/internal/node"
	"goveto-edge/internal/publisher"
	"goveto-edge/internal/purge"
	"goveto-edge/internal/settings"
	"goveto-edge/internal/storage/gen/client"
)

func New(db *sql.DB, orm *client.Client, redisClient *redis.Client, sessions *auth.SessionStore, credentialCipher *node.CredentialCipher, publishService *publisher.Service, purgeService *purge.Service) *echo.Echo {
	e := echo.New()
	e.Use(sessions.Session)
	settingStore := settings.New(orm)
	captchaVerifier := captcha.New()
	installQueue := node.NewInstallQueue(redisClient, 15*time.Minute)

	health.Register(e, db)
	authapi.Register(e, orm, sessions, settingStore, captchaVerifier)
	clusters.Register(e, orm)
	certificates.Register(e, orm)
	nodes.Register(e, orm, installQueue, credentialCipher)
	publishapi.Register(e, orm, publishService)
	purgeapi.Register(e, orm, purgeService)
	sites.Register(e, orm, publishService)

	return e
}
