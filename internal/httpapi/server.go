// Package httpapi assembles all control-plane HTTP API modules.
package httpapi

import (
	"database/sql"

	"github.com/labstack/echo/v5"
	"github.com/redis/go-redis/v9"

	"goveto-edge/internal/analytics"
	"goveto-edge/internal/audit"
	"goveto-edge/internal/auth"
	"goveto-edge/internal/captcha"
	"goveto-edge/internal/certmanager"
	"goveto-edge/internal/dnssync"
	"goveto-edge/internal/edgecontrol"
	"goveto-edge/internal/httpapi/adminsettings"
	analyticsapi "goveto-edge/internal/httpapi/analytics"
	authapi "goveto-edge/internal/httpapi/auth"
	"goveto-edge/internal/httpapi/certificates"
	"goveto-edge/internal/httpapi/clusters"
	dnsapi "goveto-edge/internal/httpapi/dns"
	"goveto-edge/internal/httpapi/health"
	"goveto-edge/internal/httpapi/initialization"
	jobsapi "goveto-edge/internal/httpapi/jobs"
	"goveto-edge/internal/httpapi/nodes"
	publishapi "goveto-edge/internal/httpapi/publish"
	purgeapi "goveto-edge/internal/httpapi/purge"
	"goveto-edge/internal/httpapi/sites"
	"goveto-edge/internal/httpapi/types"
	"goveto-edge/internal/httpsecurity"
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
	authority *edgecontrol.Authority,
	gateway *edgecontrol.Gateway,
	installQueue *node.InstallQueue,
	publishService *publisher.Service,
	certificateService *certmanager.Service,
	purgeService *purge.Service,
	dnsService *dnssync.Service,
	redisClient *redis.Client,
	securityOptions httpsecurity.Options,
	restartControlPlane func(),
	analyticsStore ...*analytics.Store,
) *echo.Echo {
	e := echo.New()
	e.HTTPErrorHandler = types.HTTPErrorHandler
	if securityOptions.IPExtractor != nil {
		e.IPExtractor = securityOptions.IPExtractor
	}
	auditRecorder := audit.New(orm)
	settingStore := settings.New(orm, auditRecorder)
	securityOptions.SessionCookieName = sessions.CookieName()
	securityOptions.CSRFCookieName = sessions.CSRFCookieName()
	e.Use(
		httpsecurity.Middleware(securityOptions),
		sessions.Session,
		sessions.RequireActiveUser(orm),
		authapi.RequireTOTPEnrollment(settingStore),
		audit.Middleware(auditRecorder, audit.ControlPlaneRoutes),
	)

	captchaVerifier := captcha.New()
	limiter := httpsecurity.NewRateLimiter(redisClient)
	var analyticsData *analytics.Store
	if len(analyticsStore) > 0 {
		analyticsData = analyticsStore[0]
	}

	health.Register(e, db, analyticsData)
	initialization.Register(e, orm, settingStore, limiter)
	authapi.Register(e, orm, sessions, settingStore, credentialCipher, captchaVerifier, limiter)
	adminsettings.Register(e, settingStore, credentialCipher, restartControlPlane)
	clusters.Register(e, orm, sessions)
	certificates.Register(e, orm, certificateService)
	dnsapi.Register(e, orm, credentialCipher, dnsService)
	nodes.Register(e, orm, installQueue, credentialCipher, authority, gateway, dnsService)
	publishapi.Register(e, orm, publishService)
	purgeapi.Register(e, orm, purgeService)
	jobsapi.Register(e, orm, publishService)
	sites.Register(e, orm, publishService, analyticsData, redisClient)

	if analyticsData != nil {
		analyticsapi.Register(e, orm, analyticsData)
	}

	return e
}
