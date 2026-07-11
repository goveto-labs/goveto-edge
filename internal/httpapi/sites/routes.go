// Package sites registers website management endpoints.
package sites

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"golang.org/x/net/idna"

	"goveto-edge/internal/auth"
	"goveto-edge/internal/clusteraccess"
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

func Register(e *echo.Echo, db *client.Client) {
	e.POST("/api/v1/clusters/:cluster_id/sites", create(db), auth.RequireAuth, clusteraccess.Require(db))
}

func create(db *client.Client) echo.HandlerFunc {
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
			if err != nil || certificate.ClusterId != c.Param("cluster_id") {
				return echo.NewHTTPError(http.StatusBadRequest, "certificate does not belong to cluster")
			}
		}
		siteID, poolID := uuid.NewString(), uuid.NewString()
		err = db.Tx(ctx, func(tx *client.Client) error {
			emptyHeaders := json.RawMessage(`{}`)
			if _, err := tx.OriginPool.Create().Set(query.OriginPool.Id.Set(poolID), query.OriginPool.ClusterId.Set(c.Param("cluster_id")), query.OriginPool.Name.Set(input.Name), query.OriginPool.Headers.Set(emptyHeaders)).Do(ctx); err != nil {
				return err
			}
			for _, origin := range input.Origins {
				sets := []query.OriginBackendSetClause{query.OriginBackend.OriginPoolId.Set(poolID), query.OriginBackend.Protocol.Set(origin.Protocol), query.OriginBackend.Address.Set(origin.Address), query.OriginBackend.Weight.Set(origin.Weight)}
				if strings.TrimSpace(origin.HostHeader) != "" {
					sets = append(sets, query.OriginBackend.HostHeader.Set(strings.TrimSpace(origin.HostHeader)))
				}
				if _, err := tx.OriginBackend.Create().Set(sets...).Do(ctx); err != nil {
					return err
				}
			}
			if _, err := tx.Site.Create().Set(query.Site.Id.Set(siteID), query.Site.ClusterId.Set(c.Param("cluster_id")), query.Site.CreatorId.Set(auth.CurrentUID(c)), query.Site.Name.Set(input.Name), query.Site.Status.Set(model.SiteStatusACTIVE), query.Site.OriginPoolId.Set(poolID)).Do(ctx); err != nil {
				return err
			}
			for _, domain := range domains {
				if _, err := tx.SiteDomain.Create().Set(query.SiteDomain.SiteId.Set(siteID), query.SiteDomain.Hostname.Set(domain)).Do(ctx); err != nil {
					return err
				}
			}
			for _, certificateID := range input.CertificateIDs {
				if _, err := tx.SiteCertificate.Create().Set(query.SiteCertificate.SiteId.Set(siteID), query.SiteCertificate.CertificateId.Set(certificateID)).Do(ctx); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		return c.JSON(http.StatusCreated, map[string]any{"id": siteID, "name": input.Name, "status": model.SiteStatusACTIVE})
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
