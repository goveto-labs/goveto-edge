// Package sites registers website management endpoints.
package sites

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"

	"golang.org/x/net/idna"

	"goveto-edge/internal/analytics"
	"goveto-edge/internal/auth"
	"goveto-edge/internal/clusteraccess"
	"goveto-edge/internal/httpapi/types"
	"goveto-edge/internal/publisher"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

type originInput struct {
	Protocol   model.OriginProtocol `json:"protocol"`
	Address    string               `json:"address"`
	HostHeader string               `json:"host_header"`
	Weight     int                  `json:"weight"`
}
type createRequest struct {
	Name           string        `json:"name"`
	Domains        []string      `json:"domains"`
	CertificateIDs []string      `json:"certificate_ids"`
	Origins        []originInput `json:"origins"`
}
type createResponse struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Status       model.SiteStatus  `json:"status"`
	PublishJob   *types.PublishJob `json:"publish_job,omitempty"`
	PublishError string            `json:"publish_error,omitempty"`
}
type siteSummary struct {
	ID               string           `json:"id"`
	Name             string           `json:"name"`
	Status           model.SiteStatus `json:"status"`
	Domains          []string         `json:"domains"`
	CertificateCount int              `json:"certificate_count"`
	BandwidthBPS     uint64           `json:"bandwidth_bps"`
	QPS              float64          `json:"qps"`
	Version          int64            `json:"version"`
	UpdatedAt        time.Time        `json:"updated_at"`
}

func Register(e *echo.Echo, db *client.Client, publishService *publisher.Service, analyticsStore *analytics.Store) {
	e.GET("/api/v1/clusters/:cluster_id/sites", list(db, analyticsStore), auth.RequireAuth, clusteraccess.Require(db))
	e.POST("/api/v1/clusters/:cluster_id/sites", create(db, publishService), auth.RequireAuth, clusteraccess.Require(db))
	e.GET("/api/v1/clusters/:cluster_id/sites/:site_id", getDetails(db), auth.RequireAuth, clusteraccess.Require(db))
	e.PATCH("/api/v1/clusters/:cluster_id/sites/:site_id", updateDetails(db, publishService), auth.RequireAuth, clusteraccess.Require(db))
	e.DELETE("/api/v1/clusters/:cluster_id/sites/:site_id", deleteSite(db), auth.RequireAuth, clusteraccess.Require(db))
	e.GET("/api/v1/clusters/:cluster_id/sites/:site_id/listener", getListener(db), auth.RequireAuth, clusteraccess.Require(db))
	e.PATCH("/api/v1/clusters/:cluster_id/sites/:site_id/listener", updateListener(db, publishService), auth.RequireAuth, clusteraccess.Require(db))
	e.GET("/api/v1/clusters/:cluster_id/sites/:site_id/cache", getCache(db), auth.RequireAuth, clusteraccess.Require(db))
	e.PUT("/api/v1/clusters/:cluster_id/sites/:site_id/cache", updateCache(db, publishService), auth.RequireAuth, clusteraccess.Require(db))
	e.GET("/api/v1/clusters/:cluster_id/sites/:site_id/compression", getCompression(db), auth.RequireAuth, clusteraccess.Require(db))
	e.PUT("/api/v1/clusters/:cluster_id/sites/:site_id/compression", updateCompression(db, publishService), auth.RequireAuth, clusteraccess.Require(db))
	e.GET("/api/v1/clusters/:cluster_id/sites/:site_id/security", getSecurity(db), auth.RequireAuth, clusteraccess.Require(db))
	e.PUT("/api/v1/clusters/:cluster_id/sites/:site_id/security", updateSecurity(db, publishService), auth.RequireAuth, clusteraccess.Require(db))
}

// @summary List sites
// @description List sites in a cluster with their domains and certificate count.
// @Tags sites
func list(db *client.Client, analyticsStore *analytics.Store) echo.HandlerFunc {
	return func(c *echo.Context) error {
		items, err := db.Site.Query().
			Where(query.Site.ClusterId.Equals(c.Param("cluster_id"))).
			OrderBy(query.Site.UpdatedAt.Desc()).
			Do(c.Request().Context())
		if err != nil {
			return err
		}
		siteIDs := make([]string, len(items))
		for index := range items {
			siteIDs[index] = items[index].Id
		}
		domainsBySite := make(map[string][]string, len(items))
		certificatesBySite := make(map[string]int, len(items))
		if len(siteIDs) > 0 {
			domains, queryErr := db.SiteDomain.Query().
				Where(query.SiteDomain.SiteId.In(siteIDs...)).
				OrderBy(query.SiteDomain.CreatedAt.Asc()).
				Do(c.Request().Context())
			if queryErr != nil {
				return queryErr
			}
			for index := range domains {
				domain := domains[index]
				domainsBySite[domain.SiteId] = append(domainsBySite[domain.SiteId], domain.Hostname)
			}
			certificates, queryErr := db.SiteCertificate.Query().
				Where(query.SiteCertificate.SiteId.In(siteIDs...)).
				Do(c.Request().Context())
			if queryErr != nil {
				return queryErr
			}
			for index := range certificates {
				certificatesBySite[certificates[index].SiteId]++
			}
		}
		ratesBySite := make(map[string]analytics.SiteRate, len(items))
		if analyticsStore != nil {
			rates, rateErr := analyticsStore.LatestSiteRates(c.Request().Context(), c.Param("cluster_id"))
			if rateErr == nil {
				for index := range rates {
					ratesBySite[rates[index].SiteID] = rates[index]
				}
			}
		}
		result := make([]siteSummary, len(items))
		for index := range items {
			rate := ratesBySite[items[index].Id]
			result[index] = siteSummary{
				ID:               items[index].Id,
				Name:             items[index].Name,
				Status:           items[index].Status,
				Domains:          domainsBySite[items[index].Id],
				CertificateCount: certificatesBySite[items[index].Id],
				BandwidthBPS:     rate.BandwidthBPS,
				QPS:              rate.QPS,
				Version:          items[index].Version,
				UpdatedAt:        items[index].UpdatedAt,
			}
		}
		return types.JSON(c, http.StatusOK, result)
	}
}

// @summary Create site
// @description Create a site with domains, origins and certificates; enqueues publish.
// @Tags sites
func create(db *client.Client, publishService *publisher.Service) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input createRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}

		input.Name = strings.TrimSpace(input.Name)
		if input.Name == "" || len(input.Domains) == 0 || len(input.Origins) == 0 {
			return echo.NewHTTPError(http.StatusBadRequest, "name, domains and origins are required")
		}

		domains, err := normalizeDomains(input.Domains)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		for index := range input.Origins {
			address, err := normalizeOrigin(input.Origins[index].Protocol, input.Origins[index].Address)
			if err != nil {
				return echo.NewHTTPError(http.StatusBadRequest, err.Error())
			}
			input.Origins[index].Address = address
			if input.Origins[index].Weight <= 0 {
				input.Origins[index].Weight = 1
			}
		}

		ctx := c.Request().Context()
		for _, certificateID := range input.CertificateIDs {
			certificate, err := db.Certificate.FindUnique(ctx, query.Certificate.Id.Equals(certificateID))
			if err != nil {
				return err
			}
			if certificate == nil || certificate.ClusterId != c.Param("cluster_id") {
				return echo.NewHTTPError(http.StatusBadRequest, "certificate does not belong to cluster")
			}
		}

		siteID, poolID := uuid.NewString(), uuid.NewString()
		err = db.Tx(ctx, func(tx *client.Client) error {
			cluster, clusterErr := tx.Cluster.FindUnique(
				ctx,
				query.Cluster.Id.Equals(c.Param("cluster_id")),
			)
			if clusterErr != nil {
				return clusterErr
			}
			if cluster == nil {
				return echo.NewHTTPError(http.StatusNotFound, "cluster not found")
			}
			for _, domain := range domains {
				if cluster.PrimaryHostname != nil && domain == *cluster.PrimaryHostname {
					return echo.NewHTTPError(http.StatusBadRequest, "site domain cannot equal the cluster primary hostname")
				}
			}

			emptyHeaders := json.RawMessage(`{}`)
			if _, err := tx.OriginPool.Create().
				Set(
					query.OriginPool.Id.Set(poolID),
					query.OriginPool.ClusterId.Set(c.Param("cluster_id")),
					query.OriginPool.Name.Set(input.Name),
					query.OriginPool.Headers.Set(emptyHeaders),
				).
				Do(ctx); err != nil {
				return err
			}

			for _, origin := range input.Origins {
				sets := []query.OriginBackendSetClause{
					query.OriginBackend.OriginPoolId.Set(poolID),
					query.OriginBackend.Protocol.Set(origin.Protocol),
					query.OriginBackend.Address.Set(origin.Address),
					query.OriginBackend.Weight.Set(origin.Weight),
				}
				if strings.TrimSpace(origin.HostHeader) != "" {
					sets = append(sets, query.OriginBackend.HostHeader.Set(
						strings.TrimSpace(origin.HostHeader),
					))
				}
				if _, err := tx.OriginBackend.Create().Set(sets...).Do(ctx); err != nil {
					return err
				}
			}

			if _, err := tx.Site.Create().
				Set(
					query.Site.Id.Set(siteID),
					query.Site.ClusterId.Set(c.Param("cluster_id")),
					query.Site.CreatorId.Set(auth.CurrentUID(c)),
					query.Site.Name.Set(input.Name),
					query.Site.Status.Set(model.SiteStatusACTIVE),
					query.Site.OriginPoolId.Set(poolID),
				).
				Do(ctx); err != nil {
				return err
			}

			listenerSets := []query.SiteListenerConfigSetClause{
				query.SiteListenerConfig.SiteId.Set(siteID),
			}
			if len(input.CertificateIDs) == 0 {
				listenerSets = append(listenerSets,
					query.SiteListenerConfig.RedirectHttpToHttps.Set(false),
					query.SiteListenerConfig.HttpsEnabled.Set(false),
					query.SiteListenerConfig.Http2Enabled.Set(false),
					query.SiteListenerConfig.Http3Enabled.Set(false),
					query.SiteListenerConfig.OcspStaplingEnabled.Set(false),
				)
			}
			if _, err := tx.SiteListenerConfig.Create().
				Set(listenerSets...).
				Do(ctx); err != nil {
				return err
			}

			for _, domain := range domains {
				if _, err := tx.SiteDomain.Create().
					Set(
						query.SiteDomain.SiteId.Set(siteID),
						query.SiteDomain.Hostname.Set(domain),
					).
					Do(ctx); err != nil {
					return err
				}
			}

			for _, certificateID := range input.CertificateIDs {
				if _, err := tx.SiteCertificate.Create().
					Set(
						query.SiteCertificate.SiteId.Set(siteID),
						query.SiteCertificate.CertificateId.Set(certificateID),
					).
					Do(ctx); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}

		response := createResponse{ID: siteID, Name: input.Name, Status: model.SiteStatusACTIVE}
		if job, publishErr := publishService.Enqueue(ctx, siteID); publishErr == nil {
			value := types.NewPublishJob(job)
			response.PublishJob = &value
		} else {
			response.PublishError = publishErr.Error()
		}
		return types.JSON(c, http.StatusCreated, response)
	}
}

func normalizeDomains(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		domain, err := idna.Lookup.ToASCII(strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), "."))
		if err != nil || domain == "" || strings.ContainsAny(domain, "/: ") {
			return nil, fmt.Errorf("invalid domain %q", value)
		}
		if _, ok := seen[domain]; ok {
			return nil, fmt.Errorf("duplicate domain %q", domain)
		}
		seen[domain] = struct{}{}
		result = append(result, domain)
	}
	return result, nil
}

func normalizeOrigin(protocol model.OriginProtocol, value string) (string, error) {
	value = strings.TrimSpace(value)
	if protocol != model.OriginProtocolHTTP && protocol != model.OriginProtocolHTTPS {
		return "", fmt.Errorf("invalid origin protocol")
	}
	if host, port, err := net.SplitHostPort(value); err == nil {
		if host == "" || port == "" {
			return "", fmt.Errorf("invalid origin address")
		}
		return net.JoinHostPort(host, port), nil
	}

	port := "80"
	if protocol == model.OriginProtocolHTTPS {
		port = "443"
	}
	if ip := net.ParseIP(value); ip != nil {
		return net.JoinHostPort(value, port), nil
	}
	if strings.Contains(value, ":") {
		return "", fmt.Errorf("invalid origin address %q", value)
	}

	host, err := idna.Lookup.ToASCII(value)
	if err != nil || host == "" {
		return "", fmt.Errorf("invalid origin address %q", value)
	}
	return net.JoinHostPort(host, port), nil
}
