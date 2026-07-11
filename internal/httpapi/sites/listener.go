package sites

import (
	"fmt"
	"net/http"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

type listenerUpdateRequest struct {
	HTTPEnabled           *bool                `json:"http_enabled"`
	HTTPPort              *int                 `json:"http_port"`
	RedirectHTTPToHTTPS   *bool                `json:"redirect_http_to_https"`
	HTTPSEnabled          *bool                `json:"https_enabled"`
	HTTPSPort             *int                 `json:"https_port"`
	HTTP2Enabled          *bool                `json:"http2_enabled"`
	HTTP3Enabled          *bool                `json:"http3_enabled"`
	TLSMinVersion         *model.TLSMinVersion `json:"tls_min_version"`
	HSTSEnabled           *bool                `json:"hsts_enabled"`
	HSTSMaxAge            *int                 `json:"hsts_max_age"`
	HSTSIncludeSubdomains *bool                `json:"hsts_include_subdomains"`
	HSTSPreload           *bool                `json:"hsts_preload"`
	OCSPStaplingEnabled   *bool                `json:"ocsp_stapling_enabled"`
}

func getListener(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if err := ensureSiteInCluster(c, db); err != nil {
			return err
		}
		config, err := db.SiteListenerConfig.FindUnique(c.Request().Context(), query.SiteListenerConfig.SiteId.Equals(c.Param("site_id")))
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, config)
	}
}

func updateListener(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if err := ensureSiteInCluster(c, db); err != nil {
			return err
		}
		current, err := db.SiteListenerConfig.FindUnique(c.Request().Context(), query.SiteListenerConfig.SiteId.Equals(c.Param("site_id")))
		if err != nil {
			return err
		}
		var input listenerUpdateRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		sets := make([]query.SiteListenerConfigSetClause, 0, 13)
		if input.HTTPEnabled != nil {
			current.HttpEnabled = *input.HTTPEnabled
			sets = append(sets, query.SiteListenerConfig.HttpEnabled.Set(*input.HTTPEnabled))
		}
		if input.HTTPPort != nil {
			current.HttpPort = *input.HTTPPort
			sets = append(sets, query.SiteListenerConfig.HttpPort.Set(*input.HTTPPort))
		}
		if input.RedirectHTTPToHTTPS != nil {
			current.RedirectHttpToHttps = *input.RedirectHTTPToHTTPS
			sets = append(sets, query.SiteListenerConfig.RedirectHttpToHttps.Set(*input.RedirectHTTPToHTTPS))
		}
		if input.HTTPSEnabled != nil {
			current.HttpsEnabled = *input.HTTPSEnabled
			sets = append(sets, query.SiteListenerConfig.HttpsEnabled.Set(*input.HTTPSEnabled))
		}
		if input.HTTPSPort != nil {
			current.HttpsPort = *input.HTTPSPort
			sets = append(sets, query.SiteListenerConfig.HttpsPort.Set(*input.HTTPSPort))
		}
		if input.HTTP2Enabled != nil {
			current.Http2Enabled = *input.HTTP2Enabled
			sets = append(sets, query.SiteListenerConfig.Http2Enabled.Set(*input.HTTP2Enabled))
		}
		if input.HTTP3Enabled != nil {
			current.Http3Enabled = *input.HTTP3Enabled
			sets = append(sets, query.SiteListenerConfig.Http3Enabled.Set(*input.HTTP3Enabled))
		}
		if input.TLSMinVersion != nil {
			current.TlsMinVersion = *input.TLSMinVersion
			sets = append(sets, query.SiteListenerConfig.TlsMinVersion.Set(*input.TLSMinVersion))
		}
		if input.HSTSEnabled != nil {
			current.HstsEnabled = *input.HSTSEnabled
			sets = append(sets, query.SiteListenerConfig.HstsEnabled.Set(*input.HSTSEnabled))
		}
		if input.HSTSMaxAge != nil {
			current.HstsMaxAge = *input.HSTSMaxAge
			sets = append(sets, query.SiteListenerConfig.HstsMaxAge.Set(*input.HSTSMaxAge))
		}
		if input.HSTSIncludeSubdomains != nil {
			current.HstsIncludeSubdomains = *input.HSTSIncludeSubdomains
			sets = append(sets, query.SiteListenerConfig.HstsIncludeSubdomains.Set(*input.HSTSIncludeSubdomains))
		}
		if input.HSTSPreload != nil {
			current.HstsPreload = *input.HSTSPreload
			sets = append(sets, query.SiteListenerConfig.HstsPreload.Set(*input.HSTSPreload))
		}
		if input.OCSPStaplingEnabled != nil {
			current.OcspStaplingEnabled = *input.OCSPStaplingEnabled
			sets = append(sets, query.SiteListenerConfig.OcspStaplingEnabled.Set(*input.OCSPStaplingEnabled))
		}
		if len(sets) == 0 {
			return echo.NewHTTPError(http.StatusBadRequest, "no settings supplied")
		}
		if err := validateListener(current); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		updated, err := db.SiteListenerConfig.Update().Where(query.SiteListenerConfig.SiteId.Equals(c.Param("site_id"))).Set(sets...).Do(c.Request().Context())
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, updated)
	}
}

func ensureSiteInCluster(c *echo.Context, db *client.Client) error {
	site, err := db.Site.FindUnique(c.Request().Context(), query.Site.Id.Equals(c.Param("site_id")))
	if err != nil || site.ClusterId != c.Param("cluster_id") {
		return echo.NewHTTPError(http.StatusNotFound, "site not found")
	}
	return nil
}

func validateListener(config *model.SiteListenerConfig) error {
	if config.HttpPort < 1 || config.HttpPort > 65535 || config.HttpsPort < 1 || config.HttpsPort > 65535 {
		return fmt.Errorf("HTTP and HTTPS ports must be between 1 and 65535")
	}
	if config.HttpEnabled && config.HttpsEnabled && config.HttpPort == config.HttpsPort {
		return fmt.Errorf("HTTP and HTTPS ports must differ")
	}
	if config.RedirectHttpToHttps && (!config.HttpEnabled || !config.HttpsEnabled) {
		return fmt.Errorf("HTTP to HTTPS redirect requires both HTTP and HTTPS")
	}
	if (config.Http2Enabled || config.Http3Enabled || config.HstsEnabled || config.OcspStaplingEnabled) && !config.HttpsEnabled {
		return fmt.Errorf("HTTP/2, HTTP/3, HSTS and OCSP require HTTPS")
	}
	if config.HstsMaxAge < 0 {
		return fmt.Errorf("HSTS max age cannot be negative")
	}
	if config.HstsPreload && (!config.HstsEnabled || !config.HstsIncludeSubdomains || config.HstsMaxAge < 31536000) {
		return fmt.Errorf("HSTS preload requires HSTS, includeSubdomains and max age of at least 31536000")
	}
	if config.TlsMinVersion != model.TLSMinVersionTLS1_2 && config.TlsMinVersion != model.TLSMinVersionTLS1_3 {
		return fmt.Errorf("TLS minimum version must be TLS1_2 or TLS1_3")
	}
	return nil
}
