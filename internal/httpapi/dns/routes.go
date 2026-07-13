// Package dns exposes cluster DNS management endpoints.
package dns

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"

	"golang.org/x/net/idna"
	"goveto-edge/internal/httpapi/types"

	"goveto-edge/internal/auth"
	"goveto-edge/internal/clusteraccess"
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
	group.PUT("", updateConfig(db, cipher, service))
	group.DELETE("", disableConfig(db, service))
	group.GET("/records", listRecords(db))
	group.GET("/jobs", listJobs(db))
	group.POST("/sync", syncNow(db, service))
	group.POST("/lines", createLine(db, service))
	group.DELETE("/lines/:line_id", deleteLine(db, service))
}

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
		if provider == nil {
			return types.JSON(c, http.StatusOK, map[string]any{"primary_hostname": cluster.PrimaryHostname, "provider": nil})
		}
		return types.JSON(c, http.StatusOK, map[string]any{
			"primary_hostname": cluster.PrimaryHostname,
			"provider": map[string]any{
				"type":                   provider.Provider,
				"zone":                   provider.Zone,
				"zone_id":                provider.ZoneId,
				"default_ttl":            provider.DefaultTtl,
				"proxied":                provider.Proxied,
				"enabled":                provider.Enabled,
				"credentials_configured": provider.CredentialsEncrypted != "",
			},
		})
	}
}

func updateConfig(
	db *client.Client,
	cipher *node.CredentialCipher,
	service *dnssync.Service,
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

		credentialsProvided := len(input.Credentials) > 0
		encryptedInput := ""
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
			encryptedInput, err = cipher.Encrypt(string(raw))
			if err != nil {
				return err
			}
		}

		enabled := true
		if input.Enabled != nil {
			enabled = *input.Enabled
		}
		ctx := c.Request().Context()
		clusterID := c.Param("cluster_id")
		var job *model.DNSSyncJob
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

			outside, queryErr := client.Raw[countRow](
				ctx,
				tx,
				`SELECT COUNT(*) AS count
				 FROM site_domains d
				 JOIN sites s ON s.id=d.site_id
				 WHERE s.cluster_id=$1
				   AND d.hostname<>$2
				   AND RIGHT(d.hostname, CHAR_LENGTH($2)+1)<>'.'||$2`,
				clusterID,
				zone,
			)
			if queryErr != nil {
				return queryErr
			}
			if len(outside) > 0 && outside[0].Count > 0 {
				return echo.NewHTTPError(
					http.StatusBadRequest,
					"all managed site domains must belong to the configured DNS zone",
				)
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

			if cancelErr := service.CancelActiveTx(
				ctx,
				tx,
				clusterID,
				"DNS configuration changed",
			); cancelErr != nil {
				return cancelErr
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

			action := model.DNSSyncActionRECONCILE
			if !enabled {
				action = model.DNSSyncActionDELETE_CLUSTER
			}
			var enqueueErr error
			job, enqueueErr = service.EnqueueTx(ctx, tx, clusterID, nil, action)
			return enqueueErr
		})
		if err != nil {
			return err
		}
		return types.JSON(c, http.StatusOK, map[string]any{"primary_hostname": host, "sync_job": job})
	}
}

func disableConfig(db *client.Client, service *dnssync.Service) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if err := requireOwner(c, db); err != nil {
			return err
		}
		ctx := c.Request().Context()
		clusterID := c.Param("cluster_id")
		var job *model.DNSSyncJob
		err := db.Tx(ctx, func(tx *client.Client) error {
			if lockErr := dnssync.LockClusterTx(ctx, tx, clusterID); lockErr != nil {
				return lockErr
			}
			config, findErr := tx.DNSProviderConfig.FindUnique(
				ctx,
				query.DNSProviderConfig.ClusterId.Equals(clusterID),
			)
			if findErr != nil {
				return findErr
			}
			if config == nil {
				return echo.NewHTTPError(http.StatusNotFound, "DNS provider is not configured")
			}
			if cancelErr := service.CancelActiveTx(
				ctx,
				tx,
				clusterID,
				"DNS was disabled",
			); cancelErr != nil {
				return cancelErr
			}
			if _, updateErr := tx.DNSProviderConfig.Update().
				Where(query.DNSProviderConfig.ClusterId.Equals(clusterID)).
				Set(
					query.DNSProviderConfig.Enabled.Set(false),
					query.DNSProviderConfig.UpdatedAt.Set(time.Now()),
				).
				Do(ctx); updateErr != nil {
				return updateErr
			}
			var enqueueErr error
			job, enqueueErr = service.EnqueueTx(
				ctx,
				tx,
				clusterID,
				nil,
				model.DNSSyncActionDELETE_CLUSTER,
			)
			return enqueueErr
		})
		if err != nil {
			return err
		}
		return types.JSON(c, http.StatusAccepted, job)
	}
}

func listRecords(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		items, err := db.DNSManagedRecord.Query().
			Where(query.DNSManagedRecord.ClusterId.Equals(c.Param("cluster_id"))).
			OrderBy(query.DNSManagedRecord.Hostname.Asc()).
			Do(c.Request().Context())
		if err != nil {
			return err
		}
		return types.JSON(c, http.StatusOK, items)
	}
}

func listJobs(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		items, err := db.DNSSyncJob.Query().
			Where(query.DNSSyncJob.ClusterId.Equals(c.Param("cluster_id"))).
			OrderBy(query.DNSSyncJob.CreatedAt.Desc()).
			Take(100).
			Do(c.Request().Context())
		if err != nil {
			return err
		}
		return types.JSON(c, http.StatusOK, items)
	}
}

func syncNow(db *client.Client, service *dnssync.Service) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if err := requireOwner(c, db); err != nil {
			return err
		}
		ctx := c.Request().Context()
		clusterID := c.Param("cluster_id")
		var job *model.DNSSyncJob
		err := db.Tx(ctx, func(tx *client.Client) error {
			if lockErr := dnssync.LockClusterTx(ctx, tx, clusterID); lockErr != nil {
				return lockErr
			}
			config, findErr := tx.DNSProviderConfig.FindUnique(
				ctx,
				query.DNSProviderConfig.ClusterId.Equals(clusterID),
			)
			if findErr != nil {
				return findErr
			}
			if config == nil {
				return echo.NewHTTPError(http.StatusConflict, "DNS provider is not configured")
			}
			if !config.Enabled {
				return echo.NewHTTPError(http.StatusConflict, "DNS provider is disabled")
			}
			cluster, findErr := tx.Cluster.FindUnique(ctx, query.Cluster.Id.Equals(clusterID))
			if findErr != nil {
				return findErr
			}
			if cluster == nil || cluster.PrimaryHostname == nil || *cluster.PrimaryHostname == "" {
				return echo.NewHTTPError(
					http.StatusConflict,
					"cluster primary hostname is not configured",
				)
			}
			var enqueueErr error
			job, enqueueErr = service.EnqueueTx(
				ctx,
				tx,
				clusterID,
				nil,
				model.DNSSyncActionRECONCILE,
			)
			return enqueueErr
		})
		if err != nil {
			return err
		}
		return types.JSON(c, http.StatusAccepted, job)
	}
}

func createLine(db *client.Client, service *dnssync.Service) echo.HandlerFunc {
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
			config, findErr := tx.DNSProviderConfig.FindUnique(
				ctx,
				query.DNSProviderConfig.ClusterId.Equals(clusterID),
			)
			if findErr != nil {
				return findErr
			}
			if config != nil && config.Enabled {
				_, createErr = service.EnqueueTx(
					ctx,
					tx,
					clusterID,
					nil,
					model.DNSSyncActionRECONCILE,
				)
			}
			return createErr
		})
		if err != nil {
			return err
		}
		return types.JSON(c, http.StatusCreated, item)
	}
}

func deleteLine(db *client.Client, service *dnssync.Service) echo.HandlerFunc {
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
			config, findErr := tx.DNSProviderConfig.FindUnique(
				ctx,
				query.DNSProviderConfig.ClusterId.Equals(clusterID),
			)
			if findErr != nil {
				return findErr
			}
			if config != nil && config.Enabled {
				_, findErr = service.EnqueueTx(
					ctx,
					tx,
					clusterID,
					nil,
					model.DNSSyncActionRECONCILE,
				)
			}
			return findErr
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
