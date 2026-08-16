// Package httpapi assembles all control-plane HTTP API modules.
package httpapi

import (
	"context"
	"database/sql"
	"net/http"

	"github.com/labstack/echo/v5"
	"github.com/redis/go-redis/v9"

	"goveto-edge/internal/analytics"
	"goveto-edge/internal/apikey"
	"goveto-edge/internal/audit"
	"goveto-edge/internal/auth"
	"goveto-edge/internal/captcha"
	"goveto-edge/internal/certmanager"
	"goveto-edge/internal/dnssync"
	"goveto-edge/internal/edgecontrol"
	"goveto-edge/internal/httpapi/adminsettings"
	analyticsapi "goveto-edge/internal/httpapi/analytics"
	apikeysapi "goveto-edge/internal/httpapi/apikeys"
	"goveto-edge/internal/httpapi/audit"
	authapi "goveto-edge/internal/httpapi/auth"
	"goveto-edge/internal/httpapi/certificates"
	"goveto-edge/internal/httpapi/clusters"
	dnsapi "goveto-edge/internal/httpapi/dns"
	"goveto-edge/internal/httpapi/health"
	"goveto-edge/internal/httpapi/initialization"
	jobsapi "goveto-edge/internal/httpapi/jobs"
	metricsapi "goveto-edge/internal/httpapi/metrics"
	"goveto-edge/internal/httpapi/nodes"
	publishapi "goveto-edge/internal/httpapi/publish"
	purgeapi "goveto-edge/internal/httpapi/purge"
	"goveto-edge/internal/httpapi/sites"
	"goveto-edge/internal/httpapi/types"
	"goveto-edge/internal/httpapi/users"
	"goveto-edge/internal/httpsecurity"
	"goveto-edge/internal/node"
	"goveto-edge/internal/publisher"
	"goveto-edge/internal/purge"
	"goveto-edge/internal/settings"
	"goveto-edge/internal/storage/gen/client"
)

type SecretCiphers struct {
	General      *node.CredentialCipher
	DNS          *node.CredentialCipher
	Notification *node.CredentialCipher
	TOTP         *node.CredentialCipher
}

func New(
	db *sql.DB,
	orm *client.Client,
	sessions *auth.SessionStore,
	secretCiphers SecretCiphers,
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
	metricsEnabled bool,
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
	securityOptions.CSRFExempt = func(request *http.Request) bool {
		return apikey.ExtractToken(request) != ""
	}
	captchaVerifier := captcha.New()
	limiter := httpsecurity.NewRateLimiter(redisClient)
	apiKeyService := apikey.New(orm)
	if metricsEnabled {
		e.Use(metricsapi.Middleware())
	}
	e.Use(
		httpsecurity.Middleware(securityOptions),
		sessions.Session,
		// Explicit API key credentials take precedence over a loaded cookie
		// session; malformed or invalid keys never fall back to that session.
		apiKeyService.Middleware(limiter, apiKeyAuthFailureRecorder(auditRecorder)),
		sessions.RequireActiveUser(orm),
		authapi.RequireTOTPEnrollment(settingStore),
		audit.Middleware(auditRecorder, audit.ControlPlaneRoutes),
	)

	var analyticsData *analytics.Store
	if len(analyticsStore) > 0 {
		analyticsData = analyticsStore[0]
	}

	health.Register(e, db, analyticsData)
	if metricsEnabled {
		metricsapi.Register(e)
	}
	initialization.Register(e, orm, settingStore, limiter, authority, gateway)
	authapi.Register(e, orm, sessions, settingStore, secretCiphers.General, secretCiphers.TOTP, captchaVerifier, limiter)
	adminsettings.Register(e, orm, settingStore, secretCiphers.General, authority, gateway, restartControlPlane)
	clusters.Register(e, orm, sessions, secretCiphers.Notification)
	apikeysapi.Register(e, orm, apiKeyService, limiter)
	certificates.Register(e, orm, certificateService)
	dnsapi.Register(e, orm, secretCiphers.DNS, dnsService)
	nodes.Register(e, orm, installQueue, secretCiphers.General, authority, gateway, dnsService)
	publishapi.Register(e, orm, publishService)
	purgeapi.Register(e, orm, purgeService)
	jobsapi.Register(e, orm, publishService)
	sites.Register(e, orm, publishService, analyticsData)
	auditapi.Register(e, orm)
	users.Register(e, orm, sessions)

	if analyticsData != nil {
		analyticsapi.Register(e, orm, analyticsData)
	}

	return e
}

func apiKeyAuthFailureRecorder(recorder audit.Recorder) apikey.AuthFailureRecorder {
	return func(ctx context.Context, event apikey.AuthFailureEvent) error {
		actor := "api_key:" + event.Prefix
		return recorder.Record(ctx, audit.Entry{
			Actor: actor, SourceIP: event.SourceIP, UserAgent: event.UserAgent,
			Action: "auth.api_key", ResourceType: "api_key", ResourceID: event.Prefix,
			RequestID: event.RequestID, Result: audit.ResultFailure, FailureReason: event.FailureCode,
		})
	}
}
