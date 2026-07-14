// Package httpapi assembles all control-plane HTTP API modules.
package httpapi

import (
	"database/sql"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/analytics"
	"goveto-edge/internal/auth"
	"goveto-edge/internal/captcha"
	"goveto-edge/internal/dnssync"
	analyticsapi "goveto-edge/internal/httpapi/analytics"
	authapi "goveto-edge/internal/httpapi/auth"
	"goveto-edge/internal/httpapi/certificates"
	"goveto-edge/internal/httpapi/clusters"
	dnsapi "goveto-edge/internal/httpapi/dns"
	"goveto-edge/internal/httpapi/health"
	"goveto-edge/internal/httpapi/initialization"
	"goveto-edge/internal/httpapi/nodes"
	publishapi "goveto-edge/internal/httpapi/publish"
	purgeapi "goveto-edge/internal/httpapi/purge"
	"goveto-edge/internal/httpapi/sites"
	"goveto-edge/internal/httpapi/types"
	"goveto-edge/internal/node"
	"goveto-edge/internal/publisher"
	"goveto-edge/internal/purge"
	"goveto-edge/internal/settings"
	"goveto-edge/internal/storage/gen/client"
)

func New(
	db *sql.DB,
	orm *client.Client,
	sessions *auth.SessionStore,
	credentialCipher *node.CredentialCipher,
	installQueue *node.InstallQueue,
	publishService *publisher.Service,
	purgeService *purge.Service,
	dnsService *dnssync.Service,
	analyticsStore ...*analytics.Store,
) *echo.Echo {
	e := echo.New()
	e.HTTPErrorHandler = types.HTTPErrorHandler
	e.Use(sessions.Session)

	settingStore := settings.New(orm)
	captchaVerifier := captcha.New()

	health.Register(e, db)
	initialization.Register(e, orm, settingStore)
	authapi.Register(e, orm, sessions, settingStore, captchaVerifier)
	clusters.Register(e, orm, sessions)
	certificates.Register(e, orm)
	dnsapi.Register(e, orm, credentialCipher, dnsService)
	nodes.Register(e, orm, installQueue, credentialCipher, dnsService)
	publishapi.Register(e, orm, publishService)
	purgeapi.Register(e, orm, purgeService)
	sites.Register(e, orm, publishService, dnsService)

	if len(analyticsStore) > 0 {
		analyticsapi.Register(e, orm, analyticsStore[0])
	}

	return e
}
