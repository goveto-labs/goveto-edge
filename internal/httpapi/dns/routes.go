// Package dns exposes cluster DNS management endpoints.
package dns

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"

	"goveto-edge/internal/httpapi/types"

	"golang.org/x/net/idna"

	"goveto-edge/internal/auth"
	"goveto-edge/internal/clusteraccess"
	"goveto-edge/internal/dnsprovider"
	"goveto-edge/internal/dnssync"
	"goveto-edge/internal/node"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

type configRequest struct {
	PrimaryHostname string                `json:"primary_hostname"`
	Provider        model.DNSProviderType `json:"provider"`
	Zone            string                `json:"zone"`
	ZoneID          string                `json:"zone_id"`
	Credentials     map[string]string     `json:"credentials"`
	DefaultTTL      int                   `json:"default_ttl"`
	Proxied         bool                  `json:"proxied"`
	Enabled         *bool                 `json:"enabled"`
}

type lineRequest struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	ProviderCode string `json:"provider_code"`
}

type discoveryRequest struct {
	Provider    model.DNSProviderType `json:"provider"`
	Zone        string                `json:"zone"`
	ZoneID      string                `json:"zone_id"`
	Credentials map[string]string     `json:"credentials"`
}

type countRow struct {
	Count int `db:"count"`
}

func Register(
	e *echo.Echo,
	db *client.Client,
	cipher *node.CredentialCipher,
	service *dnssync.Service,
) {
	group := e.Group(
		"/api/v1/clusters/:cluster_id/dns",
		auth.RequireAuth,
		clusteraccess.Require(db),
	)
	group.GET("", getConfig(db))
	group.PUT("", updateConfig(db, cipher))
	group.DELETE("", deleteConfig(db, service))
	group.POST("/refresh", refreshConfig(db, cipher))
	group.GET("/records", listRecords(db))
	group.GET("/jobs", listJobs(db))
	group.POST("/sync", syncNow(db, service))
	group.POST("/discovery/domains", discoverDomains(db, cipher))
	group.POST("/lines", createLine(db))
	group.DELETE("/lines/:line_id", deleteLine(db))
}

// @summary Discover provider domains
// @description Validate DNS provider credentials and return domains available to the account.
// @Tags dns
func discoverDomains(db *client.Client, cipher *node.CredentialCipher) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if err := requireOwner(c, db); err != nil {
			return err
		}
		var input discoveryRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		raw, err := discoveryCredentials(c, db, cipher, input)
		if err != nil {
			return err
		}
		items, err := dnsprovider.ListDomains(c.Request().Context(), input.Provider, raw, nil)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadGateway, "failed to list provider domains: "+err.Error())
		}
		return types.JSON(c, http.StatusOK, items)
	}
}

func discoveryCredentials(
	c *echo.Context,
	db *client.Client,
	cipher *node.CredentialCipher,
	input discoveryRequest,
) ([]byte, error) {
	if input.Provider != model.DNSProviderTypeALIYUN &&
		input.Provider != model.DNSProviderTypeCLOUDFLARE {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "unsupported DNS provider")
	}
	if len(input.Credentials) > 0 {
		raw, err := json.Marshal(input.Credentials)
		if err != nil {
			return nil, err
		}
		return raw, nil
	}
	config, err := db.DNSProviderConfig.FindUnique(
		c.Request().Context(),
		query.DNSProviderConfig.ClusterId.Equals(c.Param("cluster_id")),
	)
	if err != nil {
		return nil, err
	}
	if config == nil || config.Provider != input.Provider || config.CredentialsEncrypted == "" {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "DNS provider credentials are required")
	}
	plain, err := cipher.Decrypt(config.CredentialsEncrypted)
	if err != nil {
		return nil, err
	}
	return []byte(plain), nil
}

// @summary Get DNS config
// @description Return cluster primary hostname and managed DNS provider configuration.
// @Tags dns
func getConfig(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := c.Request().Context()
		cluster, err := db.Cluster.FindUnique(ctx, query.Cluster.Id.Equals(c.Param("cluster_id")))
		if err != nil {
			return err
		}
		if cluster == nil {
			return echo.NewHTTPError(http.StatusNotFound, "cluster not found")
		}
		provider, err := db.DNSProviderConfig.FindUnique(
			ctx,
			query.DNSProviderConfig.ClusterId.Equals(cluster.Id),
		)
		if err != nil {
			return err
		}
		return types.JSON(c, http.StatusOK, types.NewDNSConfig(cluster.PrimaryHostname, provider))
	}
}

// @summary Update DNS config
// @description Configure or update managed DNS provider settings and enqueue reconciliation.
// @Tags dns
func updateConfig(
	db *client.Client,
	cipher *node.CredentialCipher,
) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if err := requireOwner(c, db); err != nil {
			return err
		}

		var input configRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		host, err := hostname(input.PrimaryHostname)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		zone, err := hostname(input.Zone)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid DNS zone")
		}
		if host != zone && !strings.HasSuffix(host, "."+zone) {
			return echo.NewHTTPError(http.StatusBadRequest, "primary_hostname must belong to zone")
		}
		if input.Provider != model.DNSProviderTypeALIYUN &&
			input.Provider != model.DNSProviderTypeCLOUDFLARE {
			return echo.NewHTTPError(http.StatusBadRequest, "unsupported DNS provider")
		}
		zoneID := strings.TrimSpace(input.ZoneID)
		if input.Provider == model.DNSProviderTypeCLOUDFLARE && zoneID == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "Cloudflare zone_id is required")
		}
		if input.DefaultTTL == 0 {
			input.DefaultTTL = 300
		}
		if input.DefaultTTL < 60 || input.DefaultTTL > 86400 {
			return echo.NewHTTPError(
				http.StatusBadRequest,
				"default_ttl must be between 60 and 86400",
			)
		}

		ctx := c.Request().Context()
		clusterID := c.Param("cluster_id")
		credentialsProvided := len(input.Credentials) > 0
		encryptedInput := ""
		var credentialsRaw []byte
		if credentialsProvided {
			sanitized := map[string]string{}
			switch input.Provider {
			case model.DNSProviderTypeALIYUN:
				accessKeyID := strings.TrimSpace(input.Credentials["access_key_id"])
				accessKeySecret := strings.TrimSpace(input.Credentials["access_key_secret"])
				if accessKeyID == "" || accessKeySecret == "" {
					return echo.NewHTTPError(
						http.StatusBadRequest,
						"Aliyun access_key_id and access_key_secret are required",
					)
				}
				sanitized["access_key_id"] = accessKeyID
				sanitized["access_key_secret"] = accessKeySecret
			case model.DNSProviderTypeCLOUDFLARE:
				apiToken := strings.TrimSpace(input.Credentials["api_token"])
				if apiToken == "" {
					return echo.NewHTTPError(
						http.StatusBadRequest,
						"Cloudflare api_token is required",
					)
				}
				sanitized["api_token"] = apiToken
			}
			raw, marshalErr := json.Marshal(sanitized)
			if marshalErr != nil {
				return marshalErr
			}
			credentialsRaw = raw
			encryptedInput, err = cipher.Encrypt(string(raw))
			if err != nil {
				return err
			}
		} else {
			existing, findErr := db.DNSProviderConfig.FindUnique(
				ctx,
				query.DNSProviderConfig.ClusterId.Equals(clusterID),
			)
			if findErr != nil {
				return findErr
			}
			if existing == nil || existing.Provider != input.Provider || existing.CredentialsEncrypted == "" {
				return echo.NewHTTPError(http.StatusBadRequest, "DNS provider credentials are required")
			}
			plain, decryptErr := cipher.Decrypt(existing.CredentialsEncrypted)
			if decryptErr != nil {
				return decryptErr
			}
			credentialsRaw = []byte(plain)
		}

		providerLines, err := dnsprovider.ListLines(
			ctx,
			input.Provider,
			zone,
			zoneID,
			credentialsRaw,
			nil,
		)
		if err != nil {
			return echo.NewHTTPError(
				http.StatusBadGateway,
				"failed to list provider DNS lines: "+err.Error(),
			)
		}

		enabled := true
		if input.Enabled != nil {
			enabled = *input.Enabled
		}
		err = db.Tx(ctx, func(tx *client.Client) error {
			if lockErr := dnssync.LockClusterTx(ctx, tx, clusterID); lockErr != nil {
				return lockErr
			}
			cluster, findClusterErr := tx.Cluster.FindUnique(
				ctx,
				query.Cluster.Id.Equals(clusterID),
			)
			if findClusterErr != nil {
				return findClusterErr
			}
			if cluster == nil {
				return echo.NewHTTPError(http.StatusNotFound, "cluster not found")
			}

			collision, queryErr := client.Raw[countRow](
				ctx,
				tx,
				`SELECT COUNT(*) AS count
				 FROM site_domains d
				 JOIN sites s ON s.id=d.site_id
				 WHERE s.cluster_id=$1 AND d.hostname=$2`,
				clusterID,
				host,
			)
			if queryErr != nil {
				return queryErr
			}
			if len(collision) > 0 && collision[0].Count > 0 {
				return echo.NewHTTPError(
					http.StatusBadRequest,
					"cluster primary hostname cannot also be a site domain",
				)
			}

			existing, findErr := tx.DNSProviderConfig.FindUnique(
				ctx,
				query.DNSProviderConfig.ClusterId.Equals(clusterID),
			)
			if findErr != nil {
				return findErr
			}
			providerChanged := existing != nil && existing.Provider != input.Provider
			if existing != nil &&
				(existing.Provider != input.Provider ||
					existing.Zone != zone ||
					value(existing.ZoneId) != zoneID) {
				count, countErr := tx.DNSManagedRecord.Query().
					Where(query.DNSManagedRecord.ClusterId.Equals(clusterID)).
					Count(ctx)
				if countErr != nil {
					return countErr
				}
				if count > 0 {
					return echo.NewHTTPError(
						http.StatusConflict,
						"disable DNS and wait for record cleanup before changing provider or zone",
					)
				}
			}

			encrypted := encryptedInput
			if encrypted == "" && existing != nil {
				encrypted = existing.CredentialsEncrypted
			}
			if encrypted == "" || (providerChanged && !credentialsProvided) {
				return echo.NewHTTPError(
					http.StatusBadRequest,
					"DNS provider credentials are required",
				)
			}

			now := time.Now()
			if _, updateErr := tx.Cluster.Update().
				Where(query.Cluster.Id.Equals(clusterID)).
				Set(
					query.Cluster.PrimaryHostname.Set(host),
					query.Cluster.UpdatedAt.Set(now),
				).
				Do(ctx); updateErr != nil {
				return updateErr
			}

			sets := []query.DNSProviderConfigSetClause{
				query.DNSProviderConfig.Provider.Set(input.Provider),
				query.DNSProviderConfig.Zone.Set(zone),
				query.DNSProviderConfig.CredentialsEncrypted.Set(encrypted),
				query.DNSProviderConfig.DefaultTtl.Set(input.DefaultTTL),
				query.DNSProviderConfig.Proxied.Set(
					input.Provider == model.DNSProviderTypeCLOUDFLARE && input.Proxied,
				),
				query.DNSProviderConfig.Enabled.Set(enabled),
				query.DNSProviderConfig.UpdatedAt.Set(now),
			}
			if zoneID != "" {
				sets = append(sets, query.DNSProviderConfig.ZoneId.Set(zoneID))
			} else {
				sets = append(sets, query.DNSProviderConfig.ZoneId.SetNull())
			}
			if existing != nil {
				if _, updateErr := tx.DNSProviderConfig.Update().
					Where(query.DNSProviderConfig.ClusterId.Equals(clusterID)).
					Set(sets...).
					Do(ctx); updateErr != nil {
					return updateErr
				}
			} else {
				sets = append(sets, query.DNSProviderConfig.ClusterId.Set(clusterID))
				if _, createErr := tx.DNSProviderConfig.Create().Set(sets...).Do(ctx); createErr != nil {
					return createErr
				}
			}
			if storeErr := storeProviderLines(ctx, tx, clusterID, providerLines, now); storeErr != nil {
				return storeErr
			}

			return nil
		})
		if err != nil {
			return err
		}
		cluster, findErr := db.Cluster.FindUnique(ctx, query.Cluster.Id.Equals(clusterID))
		if findErr != nil {
			return findErr
		}
		config, findErr := db.DNSProviderConfig.FindUnique(
			ctx,
			query.DNSProviderConfig.ClusterId.Equals(clusterID),
		)
		if findErr != nil {
			return findErr
		}
		return types.JSON(c, http.StatusOK, types.NewDNSConfig(cluster.PrimaryHostname, config))
	}
}

// @summary Delete DNS config
// @description Delete provider-managed records and remove the cluster DNS configuration.
// @Tags dns
func deleteConfig(db *client.Client, service *dnssync.Service) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if err := requireOwner(c, db); err != nil {
			return err
		}
		if err := service.DeleteConfiguration(c.Request().Context(), c.Param("cluster_id")); err != nil {
			if errors.Is(err, dnssync.ErrDNSNotConfigured) {
				return echo.NewHTTPError(http.StatusNotFound, err.Error())
			}
			return err
		}
		return types.JSON(c, http.StatusOK, nil)
	}
}

// @summary Refresh DNS domain
// @description Re-fetch the configured domain and all provider DNS lines without creating a sync job.
// @Tags dns
func refreshConfig(db *client.Client, cipher *node.CredentialCipher) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if err := requireOwner(c, db); err != nil {
			return err
		}
		ctx := c.Request().Context()
		clusterID := c.Param("cluster_id")
		config, err := db.DNSProviderConfig.FindUnique(
			ctx,
			query.DNSProviderConfig.ClusterId.Equals(clusterID),
		)
		if err != nil {
			return err
		}
		if config == nil {
			return echo.NewHTTPError(http.StatusNotFound, "DNS provider is not configured")
		}
		plain, err := cipher.Decrypt(config.CredentialsEncrypted)
		if err != nil {
			return err
		}
		raw := []byte(plain)
		domains, err := dnsprovider.ListDomains(ctx, config.Provider, raw, nil)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadGateway, "failed to list provider domains: "+err.Error())
		}
		var domain *dnsprovider.Domain
		for index := range domains {
			if strings.EqualFold(domains[index].Name, config.Zone) {
				domain = &domains[index]
				break
			}
		}
		if domain == nil {
			return echo.NewHTTPError(http.StatusNotFound, "configured domain is no longer available from the provider")
		}
		zoneID := value(config.ZoneId)
		if config.Provider == model.DNSProviderTypeCLOUDFLARE {
			zoneID = domain.ID
		}
		providerLines, err := dnsprovider.ListLines(
			ctx,
			config.Provider,
			config.Zone,
			zoneID,
			raw,
			nil,
		)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadGateway, "failed to list provider DNS lines: "+err.Error())
		}
		err = db.Tx(ctx, func(tx *client.Client) error {
			if err := dnssync.LockClusterTx(ctx, tx, clusterID); err != nil {
				return err
			}
			now := time.Now()
			if err := storeProviderLines(ctx, tx, clusterID, providerLines, now); err != nil {
				return err
			}
			if config.Provider == model.DNSProviderTypeCLOUDFLARE && zoneID != value(config.ZoneId) {
				_, err = tx.DNSProviderConfig.Update().
					Where(query.DNSProviderConfig.ClusterId.Equals(clusterID)).
					Set(
						query.DNSProviderConfig.ZoneId.Set(zoneID),
						query.DNSProviderConfig.UpdatedAt.Set(now),
					).
					Do(ctx)
				return err
			}
			return nil
		})
		if err != nil {
			return err
		}
		cluster, err := db.Cluster.FindUnique(ctx, query.Cluster.Id.Equals(clusterID))
		if err != nil {
			return err
		}
		config, err = db.DNSProviderConfig.FindUnique(
			ctx,
			query.DNSProviderConfig.ClusterId.Equals(clusterID),
		)
		if err != nil {
			return err
		}
		return types.JSON(c, http.StatusOK, types.NewDNSConfig(cluster.PrimaryHostname, config))
	}
}

// @summary List DNS records
// @description List managed DNS records for the cluster.
// @Tags dns
func listRecords(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		items, err := db.DNSManagedRecord.Query().
			Where(query.DNSManagedRecord.ClusterId.Equals(c.Param("cluster_id"))).
			OrderBy(query.DNSManagedRecord.Hostname.Asc()).
			Do(c.Request().Context())
		if err != nil {
			return err
		}
		result := make([]types.DNSRecord, len(items))
		for index := range items {
			result[index] = types.NewDNSRecord(&items[index])
		}
		return types.JSON(c, http.StatusOK, result)
	}
}

// @summary List DNS sync jobs
// @description List recent DNS sync jobs for the cluster.
// @Tags dns
func listJobs(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		items, err := db.DNSSyncJob.Query().
			Where(
				query.DNSSyncJob.ClusterId.Equals(c.Param("cluster_id")),
				query.DNSSyncJob.Action.Equals(model.DNSSyncActionUPSERT_CLUSTER),
				query.DNSSyncJob.SiteId.IsNull(),
			).
			OrderBy(query.DNSSyncJob.CreatedAt.Desc()).
			Take(100).
			Do(c.Request().Context())
		if err != nil {
			return err
		}
		result := make([]types.DNSJob, len(items))
		for index := range items {
			result[index] = types.NewDNSJob(&items[index])
		}
		return types.JSON(c, http.StatusOK, result)
	}
}

// @summary Sync DNS now
// @description Compare desired node IP records with the provider and enqueue only when they differ.
// @Tags dns
func syncNow(db *client.Client, service *dnssync.Service) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if err := requireOwner(c, db); err != nil {
			return err
		}
		job, err := service.EnqueueNodeIPIfChanged(c.Request().Context(), c.Param("cluster_id"))
		if err != nil {
			return err
		}
		if job == nil {
			return types.JSON(c, http.StatusOK, nil)
		}
		return types.JSON(c, http.StatusAccepted, types.NewDNSJob(job))
	}
}

// @summary Create DNS line
// @description Create a DNS line without creating a synchronization job.
// @Tags dns
func createLine(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if err := requireOwner(c, db); err != nil {
			return err
		}
		var input lineRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		input.Name = strings.TrimSpace(input.Name)
		input.ProviderCode = strings.ToLower(strings.TrimSpace(input.ProviderCode))
		if input.Name == "" || input.ProviderCode == "" {
			return echo.NewHTTPError(
				http.StatusBadRequest,
				"name and provider_code are required",
			)
		}
		if input.ProviderCode == "default" {
			return echo.NewHTTPError(
				http.StatusBadRequest,
				"provider_code default is reserved for unassigned nodes",
			)
		}
		if !validProviderCode(input.ProviderCode) {
			return echo.NewHTTPError(
				http.StatusBadRequest,
				"provider_code may contain only lowercase letters, numbers, underscores and hyphens",
			)
		}
		if input.ID == "" {
			input.ID = uuid.NewString()
		}

		ctx := c.Request().Context()
		clusterID := c.Param("cluster_id")
		var item *model.DNSLine
		err := db.Tx(ctx, func(tx *client.Client) error {
			if lockErr := dnssync.LockClusterTx(ctx, tx, clusterID); lockErr != nil {
				return lockErr
			}
			existing, findErr := tx.DNSLine.Query().
				Where(
					query.DNSLine.ClusterId.Equals(clusterID),
					query.DNSLine.ProviderCode.Equals(input.ProviderCode),
				).
				First(ctx)
			if findErr != nil {
				return findErr
			}
			if existing != nil {
				return echo.NewHTTPError(
					http.StatusConflict,
					"provider_code is already used by another DNS line",
				)
			}
			var createErr error
			item, createErr = tx.DNSLine.Create().
				Set(
					query.DNSLine.Id.Set(input.ID),
					query.DNSLine.ClusterId.Set(clusterID),
					query.DNSLine.Name.Set(input.Name),
					query.DNSLine.ProviderCode.Set(input.ProviderCode),
					query.DNSLine.UpdatedAt.Set(time.Now()),
				).
				Do(ctx)
			if createErr != nil {
				return createErr
			}
			return nil
		})
		if err != nil {
			return err
		}
		return types.JSON(c, http.StatusCreated, types.NewDNSLine(item))
	}
}

// @summary Delete DNS line
// @description Delete a DNS line without creating a synchronization job.
// @Tags dns
func deleteLine(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if err := requireOwner(c, db); err != nil {
			return err
		}
		ctx := c.Request().Context()
		clusterID := c.Param("cluster_id")
		err := db.Tx(ctx, func(tx *client.Client) error {
			if lockErr := dnssync.LockClusterTx(ctx, tx, clusterID); lockErr != nil {
				return lockErr
			}
			line, findErr := tx.DNSLine.FindUnique(
				ctx,
				query.DNSLine.Id.Equals(c.Param("line_id")),
			)
			if findErr != nil {
				return findErr
			}
			if line == nil || line.ClusterId != clusterID {
				return echo.NewHTTPError(http.StatusNotFound, "DNS line not found")
			}
			links, countErr := tx.NodeDNSLine.Query().
				Where(query.NodeDNSLine.DnsLineId.Equals(line.Id)).
				Count(ctx)
			if countErr != nil {
				return countErr
			}
			if links > 0 {
				return echo.NewHTTPError(
					http.StatusConflict,
					"DNS line is still assigned to nodes",
				)
			}
			records, countErr := tx.DNSManagedRecord.Query().
				Where(query.DNSManagedRecord.DnsLineId.Equals(&line.Id)).
				Count(ctx)
			if countErr != nil {
				return countErr
			}
			if records > 0 {
				return echo.NewHTTPError(
					http.StatusConflict,
					"sync DNS after removing node assignments before deleting this line",
				)
			}
			if _, deleteErr := tx.DNSLine.Delete().
				Where(query.DNSLine.Id.Equals(line.Id)).
				Do(ctx); deleteErr != nil {
				return deleteErr
			}
			return nil
		})
		if err != nil {
			return err
		}
		return types.JSON(c, http.StatusOK, nil)
	}
}

func hostname(input string) (string, error) {
	input = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(input)), ".")
	ascii, err := idna.Lookup.ToASCII(input)
	if err != nil || !validHostname(ascii) {
		return "", errors.New("invalid hostname")
	}
	return ascii, nil
}

func validHostname(host string) bool {
	if host == "" || len(host) > 253 || net.ParseIP(host) != nil {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') &&
				(character < '0' || character > '9') &&
				character != '-' {
				return false
			}
		}
	}
	return true
}

func validProviderCode(code string) bool {
	if code == "" || len(code) > 64 {
		return false
	}
	for _, character := range code {
		if (character < 'a' || character > 'z') &&
			(character < '0' || character > '9') &&
			character != '_' &&
			character != '-' {
			return false
		}
	}
	return true
}

func storeProviderLines(
	ctx context.Context,
	tx *client.Client,
	clusterID string,
	providerLines []dnsprovider.Line,
	now time.Time,
) error {
	existing, err := tx.DNSLine.Query().
		Where(query.DNSLine.ClusterId.Equals(clusterID)).
		Do(ctx)
	if err != nil {
		return err
	}
	byCode := make(map[string]*model.DNSLine, len(existing)+len(providerLines))
	names := make(map[string]string, len(existing)+len(providerLines))
	for index := range existing {
		code := strings.ToLower(strings.TrimSpace(existing[index].ProviderCode))
		byCode[code] = &existing[index]
		names[existing[index].Name] = code
	}
	items := make([]query.DNSLineCreateInput, 0, len(providerLines))
	for _, line := range providerLines {
		code := strings.ToLower(strings.TrimSpace(line.Code))
		if code == "" {
			continue
		}
		name := strings.TrimSpace(line.Name)
		if name == "" {
			name = code
		}
		if code == "default" {
			name = "默认"
		}
		if current := byCode[code]; current != nil {
			if names[current.Name] == code {
				delete(names, current.Name)
			}
			name = uniqueLineName(name, code, names)
			names[name] = code
			if current.Name != name ||
				value(current.ProviderParentCode) != line.ParentCode ||
				current.SortOrder != line.SortOrder {
				sets := []query.DNSLineSetClause{
					query.DNSLine.Name.Set(name),
					query.DNSLine.SortOrder.Set(line.SortOrder),
					query.DNSLine.UpdatedAt.Set(now),
				}
				if line.ParentCode == "" {
					sets = append(sets, query.DNSLine.ProviderParentCode.SetNull())
				} else {
					sets = append(sets, query.DNSLine.ProviderParentCode.Set(line.ParentCode))
				}
				if _, err := tx.DNSLine.Update().
					Where(query.DNSLine.Id.Equals(current.Id)).
					Set(sets...).
					Do(ctx); err != nil {
					return err
				}
			}
			continue
		}
		name = uniqueLineName(name, code, names)
		names[name] = code
		items = append(items, query.DNSLineCreateInput{
			Id:                 uuid.NewString(),
			ClusterId:          clusterID,
			Name:               name,
			ProviderCode:       code,
			ProviderParentCode: optionalCreateString(line.ParentCode),
			SortOrder:          line.SortOrder,
			CreatedAt:          now,
			UpdatedAt:          now,
		})
	}
	_, err = tx.DNSLine.BulkCreate(items).
		OnConflictDoNothing("cluster_id", "name").
		BatchSize(100).
		Do(ctx)
	return err
}

func optionalCreateString(input string) **string {
	if input == "" {
		return nil
	}
	value := input
	pointer := &value
	return &pointer
}

func uniqueLineName(name, code string, names map[string]string) string {
	if owner := names[name]; owner == "" || owner == code {
		return name
	}
	name += " (" + code + ")"
	for names[name] != "" && names[name] != code {
		name += "-"
	}
	return name
}

func value(input *string) string {
	if input == nil {
		return ""
	}
	return *input
}

func requireOwner(c *echo.Context, db *client.Client) error {
	_, owner, err := clusteraccess.Check(
		c.Request().Context(),
		db,
		c.Param("cluster_id"),
		auth.CurrentUID(c),
	)
	if err != nil {
		return err
	}
	if !owner {
		return echo.NewHTTPError(
			http.StatusForbidden,
			"only the cluster owner can change DNS settings",
		)
	}
	return nil
}
