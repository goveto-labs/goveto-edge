package sites

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"

	"goveto-edge/internal/audit"
	"goveto-edge/internal/auth"
	"goveto-edge/internal/certmanager"
	"goveto-edge/internal/edgeprotocol"
	"goveto-edge/internal/httpapi/types"
	deliverypolicy "goveto-edge/internal/policy"
	"goveto-edge/internal/publisher"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

const siteBundleSchemaVersion = 1

type siteBundle struct {
	SchemaVersion  int                             `json:"schema_version"`
	Name           string                          `json:"name"`
	Status         model.SiteStatus                `json:"status"`
	Domains        []string                        `json:"domains"`
	CertificateIDs []string                        `json:"certificate_ids,omitempty"`
	Origins        []originInput                   `json:"origins"`
	OriginPolicy   edgeprotocol.OriginPolicyConfig `json:"origin_policy"`
	Listener       edgeprotocol.ListenerConfig     `json:"listener"`
	Cache          json.RawMessage                 `json:"cache,omitempty"`
	Compression    json.RawMessage                 `json:"compression,omitempty"`
	Delivery       json.RawMessage                 `json:"delivery,omitempty"`
	WAF            json.RawMessage                 `json:"waf,omitempty"`
	Access         json.RawMessage                 `json:"access,omitempty"`
	RateLimit      json.RawMessage                 `json:"rate_limit,omitempty"`
}

type cloneRequest struct {
	Name    string   `json:"name"`
	Domains []string `json:"domains"`
}

type importRequest struct {
	Sites []siteBundle `json:"sites"`
}

type templateInput struct {
	Name   string      `json:"name"`
	SiteID string      `json:"site_id,omitempty"`
	Config *siteBundle `json:"config,omitempty"`
}

type templateResponse struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	Config    *siteBundle `json:"config,omitempty"`
	UpdatedAt time.Time   `json:"updated_at"`
}

type bulkRequest struct {
	SiteIDs  []string                       `json:"site_ids"`
	Action   string                         `json:"action"`
	Delivery *deliverypolicy.DeliveryPolicy `json:"delivery,omitempty"`
}

type bulkResult struct {
	SiteID string `json:"site_id"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
}

func exportSite(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if err := ensureSiteInCluster(c, db); err != nil {
			return err
		}
		bundle, err := loadSiteBundle(c.Request().Context(), db, c.Param("site_id"))
		if err != nil {
			return err
		}
		c.Response().Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.goveto.json"`, safeFilename(bundle.Name)))
		return types.JSON(c, http.StatusOK, bundle)
	}
}

func cloneSite(db *client.Client, publishService *publisher.Service) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if err := ensureSiteInCluster(c, db); err != nil {
			return err
		}
		var input cloneRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		bundle, err := loadSiteBundle(c.Request().Context(), db, c.Param("site_id"))
		if err != nil {
			return err
		}
		bundle = prepareCloneBundle(bundle, input)
		if bundle.Name == "" || len(bundle.Domains) == 0 {
			return echo.NewHTTPError(http.StatusBadRequest, "name and domains are required")
		}
		id, err := createSiteBundle(c.Request().Context(), db, c.Param("cluster_id"), auth.CurrentUID(c), bundle)
		if err != nil {
			return err
		}
		job, publishErr := publishService.Enqueue(c.Request().Context(), id)
		response := createResponse{ID: id, Name: bundle.Name, Status: bundle.Status}
		if publishErr != nil {
			response.PublishError = publishErr.Error()
		} else {
			value := types.NewPublishJob(job)
			response.PublishJob = &value
		}
		audit.SetChange(c, nil, response)
		return types.JSON(c, http.StatusCreated, response)
	}
}

func importSites(db *client.Client, publishService *publisher.Service) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input importRequest
		if err := c.Bind(&input); err != nil || len(input.Sites) == 0 || len(input.Sites) > 100 {
			return echo.NewHTTPError(http.StatusBadRequest, "sites must contain 1 to 100 configurations")
		}
		results := make([]bulkResult, 0, len(input.Sites))
		for _, bundle := range input.Sites {
			id, err := createSiteBundle(c.Request().Context(), db, c.Param("cluster_id"), auth.CurrentUID(c), bundle)
			if err == nil {
				_, err = publishService.Enqueue(c.Request().Context(), id)
			}
			result := bulkResult{SiteID: id, OK: err == nil}
			if err != nil {
				result.Error = err.Error()
			}
			results = append(results, result)
		}
		audit.SetChange(c, nil, results)
		return types.JSON(c, http.StatusMultiStatus, results)
	}
}

func listTemplates(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		items, err := db.SiteTemplate.Query().Where(query.SiteTemplate.ClusterId.Equals(c.Param("cluster_id"))).OrderBy(query.SiteTemplate.UpdatedAt.Desc()).Do(c.Request().Context())
		if err != nil {
			return err
		}
		result := make([]templateResponse, len(items))
		for i, item := range items {
			result[i] = templateResponse{ID: item.Id, Name: item.Name, UpdatedAt: item.UpdatedAt}
		}
		return types.JSON(c, http.StatusOK, result)
	}
}

func createTemplate(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input templateInput
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		input.Name = strings.TrimSpace(input.Name)
		if input.Name == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "name is required")
		}
		var bundle siteBundle
		var err error
		if input.SiteID != "" {
			site, findErr := db.Site.FindUnique(c.Request().Context(), query.Site.Id.Equals(input.SiteID))
			if findErr != nil || site == nil || site.ClusterId != c.Param("cluster_id") {
				return echo.NewHTTPError(http.StatusNotFound, "site not found")
			}
			bundle, err = loadSiteBundle(c.Request().Context(), db, input.SiteID)
		} else if input.Config != nil {
			bundle = *input.Config
		} else {
			return echo.NewHTTPError(http.StatusBadRequest, "site_id or config is required")
		}
		if err != nil {
			return err
		}
		if err = normalizeSiteBundlePolicies(&bundle); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		bundle.Domains, bundle.CertificateIDs = nil, nil
		encoded, err := json.Marshal(bundle)
		if err != nil {
			return err
		}
		item, err := db.SiteTemplate.Create().Set(
			query.SiteTemplate.ClusterId.Set(c.Param("cluster_id")), query.SiteTemplate.Name.Set(input.Name), query.SiteTemplate.ConfigJson.Set(encoded),
		).Do(c.Request().Context())
		if err != nil {
			return err
		}
		response := templateResponse{ID: item.Id, Name: item.Name, UpdatedAt: item.UpdatedAt}
		audit.SetChange(c, nil, response)
		return types.JSON(c, http.StatusCreated, response)
	}
}

func getTemplate(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		item, err := db.SiteTemplate.FindUnique(c.Request().Context(), query.SiteTemplate.Id.Equals(c.Param("template_id")))
		if err != nil {
			return err
		}
		if item == nil || item.ClusterId != c.Param("cluster_id") {
			return echo.NewHTTPError(http.StatusNotFound, "template not found")
		}
		var bundle siteBundle
		if err = json.Unmarshal(item.ConfigJson, &bundle); err != nil {
			return err
		}
		return types.JSON(c, http.StatusOK, templateResponse{ID: item.Id, Name: item.Name, Config: &bundle, UpdatedAt: item.UpdatedAt})
	}
}

func deleteTemplate(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		item, err := db.SiteTemplate.FindUnique(c.Request().Context(), query.SiteTemplate.Id.Equals(c.Param("template_id")))
		if err != nil {
			return err
		}
		if item == nil || item.ClusterId != c.Param("cluster_id") {
			return echo.NewHTTPError(http.StatusNotFound, "template not found")
		}
		if _, err = db.SiteTemplate.Delete().Where(query.SiteTemplate.Id.Equals(item.Id)).Do(c.Request().Context()); err != nil {
			return err
		}
		audit.SetChange(c, item, nil)
		return c.NoContent(http.StatusNoContent)
	}
}

func bulkSites(db *client.Client, publishService *publisher.Service) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input bulkRequest
		if err := c.Bind(&input); err != nil || len(input.SiteIDs) == 0 || len(input.SiteIDs) > 100 {
			return echo.NewHTTPError(http.StatusBadRequest, "site_ids must contain 1 to 100 sites")
		}
		input.Action = strings.ToUpper(strings.TrimSpace(input.Action))
		if input.Action != "ENABLE" && input.Action != "DISABLE" && input.Action != "PUBLISH" && input.Action != "SET_DELIVERY" {
			return echo.NewHTTPError(http.StatusBadRequest, "unsupported bulk action")
		}
		if input.Action == "SET_DELIVERY" {
			if input.Delivery == nil {
				return echo.NewHTTPError(http.StatusBadRequest, "delivery is required")
			}
			if err := input.Delivery.NormalizeAndValidate(); err != nil {
				return echo.NewHTTPError(http.StatusBadRequest, err.Error())
			}
		}
		results := make([]bulkResult, 0, len(input.SiteIDs))
		for _, siteID := range input.SiteIDs {
			err := applyBulkSite(c.Request().Context(), db, publishService, c.Param("cluster_id"), siteID, input)
			result := bulkResult{SiteID: siteID, OK: err == nil}
			if err != nil {
				result.Error = err.Error()
			}
			results = append(results, result)
		}
		audit.SetChange(c, nil, results)
		return types.JSON(c, http.StatusMultiStatus, results)
	}
}

func applyBulkSite(ctx context.Context, db *client.Client, publishService *publisher.Service, clusterID, siteID string, input bulkRequest) error {
	site, err := db.Site.FindUnique(ctx, query.Site.Id.Equals(siteID))
	if err != nil || site == nil || site.ClusterId != clusterID {
		return fmt.Errorf("site not found")
	}
	switch input.Action {
	case "ENABLE", "DISABLE":
		status := model.SiteStatusACTIVE
		if input.Action == "DISABLE" {
			status = model.SiteStatusDISABLED
		}
		if _, err = db.Site.Update().Where(query.Site.Id.Equals(siteID)).Set(query.Site.Status.Set(status)).Do(ctx); err != nil {
			return err
		}
	case "SET_DELIVERY":
		encoded, _ := json.Marshal(input.Delivery)
		if site.PolicyId == nil {
			empty := json.RawMessage(`{}`)
			policyID := uuid.NewString()
			if _, err = db.Policy.Create().Set(
				query.Policy.Id.Set(policyID), query.Policy.Name.Set("site:"+siteID), query.Policy.CacheJson.Set(empty),
				query.Policy.CompressionJson.Set(empty), query.Policy.DeliveryJson.Set(encoded), query.Policy.WafJson.Set(empty),
				query.Policy.CcJson.Set(empty), query.Policy.AccessJson.Set(empty),
			).Do(ctx); err != nil {
				return err
			}
			if _, err = db.Site.Update().Where(query.Site.Id.Equals(siteID)).Set(query.Site.PolicyId.Set(policyID)).Do(ctx); err != nil {
				return err
			}
		} else if _, err = db.Policy.Update().Where(query.Policy.Id.Equals(*site.PolicyId)).Set(query.Policy.DeliveryJson.Set(encoded)).Do(ctx); err != nil {
			return err
		}
	}
	_, err = publishService.Enqueue(ctx, siteID)
	return err
}

func loadSiteBundle(ctx context.Context, db *client.Client, siteID string) (siteBundle, error) {
	site, err := db.Site.FindUnique(ctx, query.Site.Id.Equals(siteID))
	if err != nil || site == nil {
		return siteBundle{}, err
	}
	pool, err := db.OriginPool.FindUnique(ctx, query.OriginPool.Id.Equals(site.OriginPoolId))
	if err != nil || pool == nil {
		return siteBundle{}, err
	}
	policy, err := edgeprotocol.DecodeOriginPolicy(pool.Governance, pool.Headers, pool.HealthUri, pool.Timeout)
	if err != nil {
		return siteBundle{}, err
	}
	bundle := siteBundle{SchemaVersion: siteBundleSchemaVersion, Name: site.Name, Status: site.Status, OriginPolicy: policy}
	domains, err := db.SiteDomain.Query().Where(query.SiteDomain.SiteId.Equals(siteID)).OrderBy(query.SiteDomain.CreatedAt.Asc()).Do(ctx)
	if err != nil {
		return bundle, err
	}
	for _, item := range domains {
		bundle.Domains = append(bundle.Domains, item.Hostname)
	}
	backends, err := db.OriginBackend.Query().Where(query.OriginBackend.OriginPoolId.Equals(pool.Id)).OrderBy(query.OriginBackend.CreatedAt.Asc()).Do(ctx)
	if err != nil {
		return bundle, err
	}
	for _, item := range backends {
		bundle.Origins = append(bundle.Origins, originInput{Protocol: item.Protocol, Address: item.Address, HostHeader: stringValue(item.HostHeader), Weight: item.Weight, Priority: item.Priority})
	}
	listener, err := db.SiteListenerConfig.FindUnique(ctx, query.SiteListenerConfig.SiteId.Equals(siteID))
	if err != nil || listener == nil {
		return bundle, err
	}
	bundle.Listener = edgeprotocol.ListenerConfig{
		HTTPEnabled: listener.HttpEnabled, HTTPPort: listener.HttpPort, RedirectHTTPToHTTPS: listener.RedirectHttpToHttps,
		HTTPSEnabled: listener.HttpsEnabled, HTTPSPort: listener.HttpsPort, HTTP2Enabled: listener.Http2Enabled, HTTP3Enabled: listener.Http3Enabled,
		TLSMinVersion: string(listener.TlsMinVersion), HSTSEnabled: listener.HstsEnabled, HSTSMaxAge: listener.HstsMaxAge,
		HSTSIncludeSubdomains: listener.HstsIncludeSubdomains, HSTSPreload: listener.HstsPreload, OCSPStaplingEnabled: listener.OcspStaplingEnabled,
	}
	links, err := db.SiteCertificate.Query().Where(query.SiteCertificate.SiteId.Equals(siteID)).Do(ctx)
	if err != nil {
		return bundle, err
	}
	for _, link := range links {
		bundle.CertificateIDs = append(bundle.CertificateIDs, link.CertificateId)
	}
	if site.PolicyId != nil {
		stored, findErr := db.Policy.FindUnique(ctx, query.Policy.Id.Equals(*site.PolicyId))
		if findErr != nil {
			return bundle, findErr
		}
		bundle.Cache, bundle.Compression, bundle.Delivery = stored.CacheJson, stored.CompressionJson, stored.DeliveryJson
		bundle.WAF, bundle.Access, bundle.RateLimit = stored.WafJson, stored.AccessJson, stored.CcJson
	}
	return bundle, nil
}

func createSiteBundle(ctx context.Context, db *client.Client, clusterID, creatorID string, bundle siteBundle) (string, error) {
	if bundle.SchemaVersion != 0 && bundle.SchemaVersion != siteBundleSchemaVersion {
		return "", fmt.Errorf("unsupported site bundle schema_version %d", bundle.SchemaVersion)
	}
	bundle.Name = strings.TrimSpace(bundle.Name)
	if bundle.Name == "" || len(bundle.Domains) == 0 || len(bundle.Origins) == 0 {
		return "", fmt.Errorf("name, domains and origins are required")
	}
	domains, err := normalizeDomains(bundle.Domains)
	if err != nil {
		return "", err
	}
	for i := range bundle.Origins {
		address, normalizeErr := normalizeOrigin(bundle.Origins[i].Protocol, bundle.Origins[i].Address)
		if normalizeErr != nil {
			return "", normalizeErr
		}
		bundle.Origins[i].Address = address
		if bundle.Origins[i].Weight <= 0 {
			bundle.Origins[i].Weight = 1
		}
		if bundle.Origins[i].Priority < 0 {
			return "", fmt.Errorf("origin priority must not be negative")
		}
	}
	bundle.OriginPolicy = edgeprotocol.NormalizeOriginPolicy(bundle.OriginPolicy)
	if err = edgeprotocol.ValidateOriginPolicy(bundle.OriginPolicy); err != nil {
		return "", err
	}
	if err = normalizeSiteBundlePolicies(&bundle); err != nil {
		return "", err
	}
	siteID, poolID, policyID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	governance, _ := json.Marshal(bundle.OriginPolicy)
	headers, _ := json.Marshal(bundle.OriginPolicy.Headers)
	status := bundle.Status
	if status == "" {
		status = model.SiteStatusACTIVE
	}
	if status != model.SiteStatusACTIVE && status != model.SiteStatusDISABLED {
		return "", fmt.Errorf("invalid site status %q", status)
	}
	err = db.Tx(ctx, func(tx *client.Client) error {
		if _, createErr := tx.OriginPool.Create().Set(
			query.OriginPool.Id.Set(poolID), query.OriginPool.ClusterId.Set(clusterID), query.OriginPool.Name.Set(bundle.Name),
			query.OriginPool.HealthUri.Set(bundle.OriginPolicy.HealthURI), query.OriginPool.Timeout.Set(bundle.OriginPolicy.TimeoutMS),
			query.OriginPool.Headers.Set(headers), query.OriginPool.Governance.Set(governance),
		).Do(ctx); createErr != nil {
			return createErr
		}
		for _, origin := range bundle.Origins {
			clauses := []query.OriginBackendSetClause{
				query.OriginBackend.OriginPoolId.Set(poolID), query.OriginBackend.Protocol.Set(origin.Protocol), query.OriginBackend.Address.Set(origin.Address),
				query.OriginBackend.Weight.Set(origin.Weight), query.OriginBackend.Priority.Set(origin.Priority),
			}
			if origin.HostHeader != "" {
				clauses = append(clauses, query.OriginBackend.HostHeader.Set(origin.HostHeader))
			}
			if _, createErr := tx.OriginBackend.Create().Set(clauses...).Do(ctx); createErr != nil {
				return createErr
			}
		}
		if _, createErr := tx.Policy.Create().Set(
			query.Policy.Id.Set(policyID), query.Policy.Name.Set("site:"+siteID),
			query.Policy.CacheJson.Set(rawOrEmpty(bundle.Cache)), query.Policy.CompressionJson.Set(rawOrEmpty(bundle.Compression)),
			query.Policy.DeliveryJson.Set(rawOrEmpty(bundle.Delivery)), query.Policy.WafJson.Set(rawOrEmpty(bundle.WAF)),
			query.Policy.CcJson.Set(rawOrEmpty(bundle.RateLimit)), query.Policy.AccessJson.Set(rawOrEmpty(bundle.Access)),
		).Do(ctx); createErr != nil {
			return createErr
		}
		if _, createErr := tx.Site.Create().Set(
			query.Site.Id.Set(siteID), query.Site.ClusterId.Set(clusterID), query.Site.CreatorId.Set(creatorID), query.Site.Name.Set(bundle.Name),
			query.Site.Status.Set(status), query.Site.OriginPoolId.Set(poolID), query.Site.PolicyId.Set(policyID),
		).Do(ctx); createErr != nil {
			return createErr
		}
		listener := bundle.Listener
		if listener.HTTPPort == 0 {
			listener.HTTPEnabled, listener.HTTPPort = true, 80
		}
		if listener.HTTPSPort == 0 {
			listener.HTTPSPort = 443
		}
		tlsVersion := model.TLSMinVersionTLS1_2
		if listener.TLSMinVersion == "TLS1_3" {
			tlsVersion = model.TLSMinVersionTLS1_3
		}
		if _, createErr := tx.SiteListenerConfig.Create().Set(
			query.SiteListenerConfig.SiteId.Set(siteID), query.SiteListenerConfig.HttpEnabled.Set(listener.HTTPEnabled),
			query.SiteListenerConfig.HttpPort.Set(listener.HTTPPort), query.SiteListenerConfig.RedirectHttpToHttps.Set(listener.RedirectHTTPToHTTPS),
			query.SiteListenerConfig.HttpsEnabled.Set(listener.HTTPSEnabled), query.SiteListenerConfig.HttpsPort.Set(listener.HTTPSPort),
			query.SiteListenerConfig.Http2Enabled.Set(listener.HTTP2Enabled), query.SiteListenerConfig.Http3Enabled.Set(listener.HTTP3Enabled),
			query.SiteListenerConfig.TlsMinVersion.Set(tlsVersion), query.SiteListenerConfig.HstsEnabled.Set(listener.HSTSEnabled),
			query.SiteListenerConfig.HstsMaxAge.Set(listener.HSTSMaxAge), query.SiteListenerConfig.HstsIncludeSubdomains.Set(listener.HSTSIncludeSubdomains),
			query.SiteListenerConfig.HstsPreload.Set(listener.HSTSPreload), query.SiteListenerConfig.OcspStaplingEnabled.Set(listener.OCSPStaplingEnabled),
		).Do(ctx); createErr != nil {
			return createErr
		}
		for _, domain := range domains {
			if _, createErr := tx.SiteDomain.Create().Set(query.SiteDomain.SiteId.Set(siteID), query.SiteDomain.Hostname.Set(domain)).Do(ctx); createErr != nil {
				return createErr
			}
		}
		for _, certificateID := range bundle.CertificateIDs {
			certificate, findErr := tx.Certificate.FindUnique(ctx, query.Certificate.Id.Equals(certificateID))
			if findErr != nil || certificate == nil || certificate.ClusterId != clusterID {
				return fmt.Errorf("certificate %s does not belong to cluster", certificateID)
			}
			if certificate.CertPem == nil || certificate.ExpiresAt == nil || !time.Now().UTC().Before(*certificate.ExpiresAt) {
				return fmt.Errorf("certificate %s is not issued or has expired", certificateID)
			}
			certificateDomains, decodeErr := certmanager.DecodeDomains(certificate.DomainsJson)
			if decodeErr != nil {
				return decodeErr
			}
			if coverErr := certmanager.CoversDomains(certificateDomains, domains); coverErr != nil {
				return coverErr
			}
			if _, createErr := tx.SiteCertificate.Create().Set(query.SiteCertificate.SiteId.Set(siteID), query.SiteCertificate.CertificateId.Set(certificateID)).Do(ctx); createErr != nil {
				return createErr
			}
		}
		return nil
	})
	return siteID, err
}

func prepareCloneBundle(bundle siteBundle, input cloneRequest) siteBundle {
	bundle.Name = strings.TrimSpace(input.Name)
	bundle.Domains = input.Domains
	// Certificates are domain-bound. A clone receives new domains, so start it on HTTP until a matching certificate is attached.
	bundle.CertificateIDs = nil
	bundle.Listener.HTTPEnabled = true
	if bundle.Listener.HTTPPort == 0 {
		bundle.Listener.HTTPPort = 80
	}
	bundle.Listener.RedirectHTTPToHTTPS = false
	bundle.Listener.HTTPSEnabled = false
	bundle.Listener.HTTP2Enabled = false
	bundle.Listener.HTTP3Enabled = false
	bundle.Listener.HSTSEnabled = false
	bundle.Listener.OCSPStaplingEnabled = false
	return bundle
}

func normalizeSiteBundlePolicies(bundle *siteBundle) error {
	var err error
	bundle.Cache, err = normalizeBundlePolicy("cache", bundle.Cache, deliverypolicy.DefaultCachePolicy(), (*deliverypolicy.CachePolicy).NormalizeAndValidate)
	if err != nil {
		return err
	}
	bundle.Compression, err = normalizeBundlePolicy("compression", bundle.Compression, deliverypolicy.DefaultCompressionPolicy(), (*deliverypolicy.CompressionPolicy).NormalizeAndValidate)
	if err != nil {
		return err
	}
	bundle.Delivery, err = normalizeBundlePolicy("delivery", bundle.Delivery, deliverypolicy.DefaultDeliveryPolicy(), (*deliverypolicy.DeliveryPolicy).NormalizeAndValidate)
	if err != nil {
		return err
	}
	bundle.WAF, err = normalizeBundlePolicy("WAF", bundle.WAF, deliverypolicy.DefaultWAFPolicy(), (*deliverypolicy.WAFPolicy).NormalizeAndValidate)
	if err != nil {
		return err
	}
	bundle.Access, err = normalizeBundlePolicy("access", bundle.Access, deliverypolicy.DefaultAccessPolicy(), (*deliverypolicy.AccessPolicy).NormalizeAndValidate)
	if err != nil {
		return err
	}
	bundle.RateLimit, err = normalizeBundlePolicy("rate-limit", bundle.RateLimit, deliverypolicy.DefaultRateLimitPolicy(), (*deliverypolicy.RateLimitPolicy).NormalizeAndValidate)
	return err
}

func normalizeBundlePolicy[T any](name string, raw json.RawMessage, defaults T, validate func(*T) error) (json.RawMessage, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "{}" || trimmed == "null" {
		return json.RawMessage(`{}`), nil
	}
	value := defaults
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("decode %s policy: %w", name, err)
	}
	if err := validate(&value); err != nil {
		return nil, fmt.Errorf("invalid %s policy: %w", name, err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode %s policy: %w", name, err)
	}
	return encoded, nil
}

func rawOrEmpty(value json.RawMessage) json.RawMessage {
	if len(value) == 0 || string(value) == "null" {
		return json.RawMessage(`{}`)
	}
	return value
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func safeFilename(value string) string {
	value = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, value)
	return strings.Trim(value, "-")
}
