package sites

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/auth"
	"goveto-edge/internal/certmanager"
	"goveto-edge/internal/edgeprotocol"
	"goveto-edge/internal/httpapi/types"
	"goveto-edge/internal/publisher"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

type siteDetails struct {
	ID             string                          `json:"id"`
	ClusterID      string                          `json:"cluster_id"`
	Name           string                          `json:"name"`
	Status         model.SiteStatus                `json:"status"`
	Domains        []string                        `json:"domains"`
	CertificateIDs []string                        `json:"certificate_ids"`
	Origins        []originInput                   `json:"origins"`
	OriginPolicy   edgeprotocol.OriginPolicyConfig `json:"origin_policy"`
	Version        int64                           `json:"version"`
	CreatedAt      time.Time                       `json:"created_at"`
	UpdatedAt      time.Time                       `json:"updated_at"`
	Certificates   int                             `json:"certificate_count"`
}

type updateDetailsRequest struct {
	Name           *string                          `json:"name"`
	ClusterID      *string                          `json:"cluster_id"`
	CertificateIDs *[]string                        `json:"certificate_ids"`
	Domains        *[]string                        `json:"domains"`
	Origins        *[]originInput                   `json:"origins"`
	OriginPolicy   *edgeprotocol.OriginPolicyConfig `json:"origin_policy"`
}

func loadDetails(c *echo.Context, db *client.Client) (siteDetails, error) {
	ctx := c.Request().Context()
	site, err := db.Site.FindUnique(ctx, query.Site.Id.Equals(c.Param("site_id")))
	if err != nil {
		return siteDetails{}, err
	}
	if site == nil || site.ClusterId != c.Param("cluster_id") {
		return siteDetails{}, echo.NewHTTPError(http.StatusNotFound, "site not found")
	}
	domains, err := db.SiteDomain.Query().Where(query.SiteDomain.SiteId.Equals(site.Id)).OrderBy(query.SiteDomain.CreatedAt.Asc()).Do(ctx)
	if err != nil {
		return siteDetails{}, err
	}
	backends, err := db.OriginBackend.Query().Where(query.OriginBackend.OriginPoolId.Equals(site.OriginPoolId)).OrderBy(query.OriginBackend.CreatedAt.Asc()).Do(ctx)
	if err != nil {
		return siteDetails{}, err
	}
	pool, err := db.OriginPool.FindUnique(ctx, query.OriginPool.Id.Equals(site.OriginPoolId))
	if err != nil {
		return siteDetails{}, err
	}
	if pool == nil {
		return siteDetails{}, echo.NewHTTPError(http.StatusInternalServerError, "origin pool not found")
	}
	originPolicy, err := edgeprotocol.DecodeOriginPolicy(pool.Governance, pool.Headers, pool.HealthUri, pool.Timeout)
	if err != nil {
		return siteDetails{}, err
	}
	certificates, err := db.SiteCertificate.Query().Where(query.SiteCertificate.SiteId.Equals(site.Id)).Do(ctx)
	if err != nil {
		return siteDetails{}, err
	}
	result := siteDetails{
		ID: site.Id, ClusterID: site.ClusterId, Name: site.Name, Status: site.Status,
		Domains: make([]string, len(domains)), CertificateIDs: make([]string, len(certificates)), Origins: make([]originInput, len(backends)),
		Version: site.Version, CreatedAt: site.CreatedAt, UpdatedAt: site.UpdatedAt,
		Certificates: len(certificates),
		OriginPolicy: originPolicy,
	}
	for index, domain := range domains {
		result.Domains[index] = domain.Hostname
	}
	for index, certificate := range certificates {
		result.CertificateIDs[index] = certificate.CertificateId
	}
	for index, backend := range backends {
		result.Origins[index] = originInput{Protocol: backend.Protocol, Address: backend.Address, Weight: backend.Weight, Priority: backend.Priority}
		if backend.HostHeader != nil {
			result.Origins[index].HostHeader = *backend.HostHeader
		}
	}
	return result, nil
}

func getDetails(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		result, err := loadDetails(c, db)
		if err != nil {
			return err
		}
		result.OriginPolicy = redactOriginPolicy(result.OriginPolicy)
		return types.JSON(c, http.StatusOK, result)
	}
}

func updateDetails(db *client.Client, publishService *publisher.Service) echo.HandlerFunc {
	return func(c *echo.Context) error {
		current, err := loadDetails(c, db)
		if err != nil {
			return err
		}
		var input updateDetailsRequest
		if err = c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		name := current.Name
		if input.Name != nil {
			name = strings.TrimSpace(*input.Name)
			if name == "" {
				return echo.NewHTTPError(http.StatusBadRequest, "name is required")
			}
		}
		domains := current.Domains
		if input.Domains != nil {
			domains, err = normalizeDomains(*input.Domains)
			if err != nil || len(domains) == 0 {
				if err != nil {
					return echo.NewHTTPError(http.StatusBadRequest, err.Error())
				}
				return echo.NewHTTPError(http.StatusBadRequest, "at least one domain is required")
			}
		}
		origins := current.Origins
		if input.Origins != nil {
			origins = *input.Origins
			if len(origins) == 0 {
				return echo.NewHTTPError(http.StatusBadRequest, "at least one origin is required")
			}
			for index := range origins {
				origins[index].Address, err = normalizeOrigin(origins[index].Protocol, origins[index].Address)
				if err != nil {
					return echo.NewHTTPError(http.StatusBadRequest, err.Error())
				}
				origins[index].HostHeader = strings.TrimSpace(origins[index].HostHeader)
				if origins[index].Weight <= 0 {
					origins[index].Weight = 1
				}
				if origins[index].Priority < 0 {
					return echo.NewHTTPError(http.StatusBadRequest, "origin priority must not be negative")
				}
			}
		}
		originPolicy := current.OriginPolicy
		if input.OriginPolicy != nil {
			candidate := preserveOriginMTLS(*input.OriginPolicy, current.OriginPolicy)
			originPolicy = edgeprotocol.NormalizeOriginPolicy(candidate)
			if err = edgeprotocol.ValidateOriginPolicy(originPolicy); err != nil {
				return echo.NewHTTPError(http.StatusBadRequest, err.Error())
			}
		}
		targetCluster := current.ClusterID
		if input.ClusterID != nil && strings.TrimSpace(*input.ClusterID) != "" {
			targetCluster = strings.TrimSpace(*input.ClusterID)
		}
		certificateIDs := current.CertificateIDs
		if input.CertificateIDs != nil {
			certificateIDs = *input.CertificateIDs
		}
		if targetCluster != current.ClusterID {
			memberships, findErr := db.ClusterMember.Query().Where(
				query.ClusterMember.ClusterId.Equals(targetCluster),
				query.ClusterMember.UserId.Equals(auth.CurrentUID(c)),
			).Do(c.Request().Context())
			if findErr != nil {
				return findErr
			}
			if len(memberships) == 0 {
				return echo.NewHTTPError(http.StatusForbidden, "target cluster access denied")
			}
			if input.CertificateIDs == nil && current.Certificates > 0 {
				return echo.NewHTTPError(http.StatusBadRequest, "remove site certificates before transferring the site")
			}
		}
		seenCertificateIDs := make(map[string]struct{}, len(certificateIDs))
		for _, certificateID := range certificateIDs {
			if _, exists := seenCertificateIDs[certificateID]; exists {
				return echo.NewHTTPError(http.StatusBadRequest, "duplicate certificate")
			}
			seenCertificateIDs[certificateID] = struct{}{}
			certificate, findErr := db.Certificate.FindUnique(c.Request().Context(), query.Certificate.Id.Equals(certificateID))
			if findErr != nil {
				return findErr
			}
			if certificate == nil || certificate.ClusterId != targetCluster {
				return echo.NewHTTPError(http.StatusBadRequest, "certificate does not belong to cluster")
			}
			if certificate.CertPem == nil || certificate.ExpiresAt == nil || !time.Now().UTC().Before(*certificate.ExpiresAt) {
				return echo.NewHTTPError(http.StatusBadRequest, "certificate is not issued or has expired")
			}
			certificateDomains, decodeErr := certmanager.DecodeDomains(certificate.DomainsJson)
			if decodeErr != nil {
				return echo.NewHTTPError(http.StatusBadRequest, decodeErr.Error())
			}
			if coverErr := certmanager.CoversDomains(certificateDomains, domains); coverErr != nil {
				return echo.NewHTTPError(http.StatusBadRequest, coverErr.Error())
			}
		}

		ctx := c.Request().Context()
		siteModel, err := db.Site.FindUnique(ctx, query.Site.Id.Equals(current.ID))
		if err != nil {
			return err
		}
		err = db.Tx(ctx, func(tx *client.Client) error {
			sets := []query.SiteSetClause{query.Site.Name.Set(name)}
			if targetCluster != current.ClusterID {
				sets = append(sets, query.Site.ClusterId.Set(targetCluster))
			}
			if _, updateErr := tx.Site.Update().Where(query.Site.Id.Equals(current.ID)).Set(sets...).Do(ctx); updateErr != nil {
				return updateErr
			}
			poolSets := []query.OriginPoolSetClause{query.OriginPool.Name.Set(name), query.OriginPool.ClusterId.Set(targetCluster)}
			if input.OriginPolicy != nil {
				governance, _ := json.Marshal(originPolicy)
				headers, _ := json.Marshal(originPolicy.Headers)
				poolSets = append(poolSets,
					query.OriginPool.HealthUri.Set(originPolicy.HealthURI),
					query.OriginPool.Timeout.Set(originPolicy.TimeoutMS),
					query.OriginPool.Headers.Set(headers),
					query.OriginPool.Governance.Set(governance),
				)
			}
			if _, updateErr := tx.OriginPool.Update().Where(query.OriginPool.Id.Equals(siteModel.OriginPoolId)).Set(poolSets...).Do(ctx); updateErr != nil {
				return updateErr
			}
			if input.Domains != nil {
				if _, deleteErr := tx.SiteDomain.Delete().Where(query.SiteDomain.SiteId.Equals(current.ID)).DoMany(ctx); deleteErr != nil {
					return deleteErr
				}
				for _, domain := range domains {
					if _, createErr := tx.SiteDomain.Create().Set(query.SiteDomain.SiteId.Set(current.ID), query.SiteDomain.Hostname.Set(domain)).Do(ctx); createErr != nil {
						return createErr
					}
				}
			}
			if input.Origins != nil {
				site, findErr := tx.Site.FindUnique(ctx, query.Site.Id.Equals(current.ID))
				if findErr != nil {
					return findErr
				}
				if _, deleteErr := tx.OriginBackend.Delete().Where(query.OriginBackend.OriginPoolId.Equals(site.OriginPoolId)).DoMany(ctx); deleteErr != nil {
					return deleteErr
				}
				for _, origin := range origins {
					clauses := []query.OriginBackendSetClause{query.OriginBackend.OriginPoolId.Set(site.OriginPoolId), query.OriginBackend.Protocol.Set(origin.Protocol), query.OriginBackend.Address.Set(origin.Address), query.OriginBackend.Weight.Set(origin.Weight), query.OriginBackend.Priority.Set(origin.Priority)}
					if origin.HostHeader != "" {
						clauses = append(clauses, query.OriginBackend.HostHeader.Set(origin.HostHeader))
					}
					if _, createErr := tx.OriginBackend.Create().Set(clauses...).Do(ctx); createErr != nil {
						return createErr
					}
				}
			}
			if input.CertificateIDs != nil {
				if _, deleteErr := tx.SiteCertificate.Delete().Where(query.SiteCertificate.SiteId.Equals(current.ID)).DoMany(ctx); deleteErr != nil {
					return deleteErr
				}
				for _, certificateID := range certificateIDs {
					if _, createErr := tx.SiteCertificate.Create().Set(
						query.SiteCertificate.SiteId.Set(current.ID),
						query.SiteCertificate.CertificateId.Set(certificateID),
					).Do(ctx); createErr != nil {
						return createErr
					}
				}
			}
			if targetCluster != current.ClusterID {
				if _, deleteErr := tx.NodeSiteConfigVersion.Delete().Where(query.NodeSiteConfigVersion.SiteId.Equals(current.ID)).DoMany(ctx); deleteErr != nil {
					return deleteErr
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		if _, publishErr := publishService.Enqueue(ctx, current.ID); publishErr != nil {
			return echo.NewHTTPError(http.StatusAccepted, "saved, but publish could not be queued: "+publishErr.Error())
		}
		current.Name = name
		current.ClusterID = targetCluster
		current.Domains = domains
		current.CertificateIDs = certificateIDs
		current.Certificates = len(certificateIDs)
		current.Origins = origins
		current.OriginPolicy = originPolicy
		current.UpdatedAt = time.Now().UTC()
		current.OriginPolicy = redactOriginPolicy(current.OriginPolicy)
		return types.JSON(c, http.StatusOK, current)
	}
}

func redactOriginPolicy(policy edgeprotocol.OriginPolicyConfig) edgeprotocol.OriginPolicyConfig {
	policy.Transport.MTLSConfigured = policy.Transport.TLSClientCertificatePEM != "" && policy.Transport.TLSClientPrivateKeyPEM != ""
	policy.Transport.TLSClientCertificatePEM = ""
	policy.Transport.TLSClientPrivateKeyPEM = ""
	return policy
}

func preserveOriginMTLS(candidate, current edgeprotocol.OriginPolicyConfig) edgeprotocol.OriginPolicyConfig {
	if candidate.Transport.MTLSConfigured && candidate.Transport.TLSClientCertificatePEM == "" && candidate.Transport.TLSClientPrivateKeyPEM == "" {
		candidate.Transport.TLSClientCertificatePEM = current.Transport.TLSClientCertificatePEM
		candidate.Transport.TLSClientPrivateKeyPEM = current.Transport.TLSClientPrivateKeyPEM
	}
	return candidate
}

func deleteSite(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		current, err := loadDetails(c, db)
		if err != nil {
			return err
		}
		ctx := c.Request().Context()
		site, err := db.Site.FindUnique(ctx, query.Site.Id.Equals(current.ID))
		if err != nil {
			return err
		}
		err = db.Tx(ctx, func(tx *client.Client) error {
			if _, e := tx.NodeSiteConfigVersion.Delete().Where(query.NodeSiteConfigVersion.SiteId.Equals(current.ID)).DoMany(ctx); e != nil {
				return e
			}
			if _, e := tx.ConfigVersion.Delete().Where(query.ConfigVersion.SiteId.Equals(current.ID)).DoMany(ctx); e != nil {
				return e
			}
			if _, e := tx.PublishJob.Delete().Where(query.PublishJob.SiteId.Equals(current.ID)).DoMany(ctx); e != nil {
				return e
			}
			if _, e := tx.PurgeJob.Delete().Where(query.PurgeJob.SiteId.Equals(current.ID)).DoMany(ctx); e != nil {
				return e
			}
			if _, e := tx.SiteCertificate.Delete().Where(query.SiteCertificate.SiteId.Equals(current.ID)).DoMany(ctx); e != nil {
				return e
			}
			if _, e := tx.SiteListenerConfig.Delete().Where(query.SiteListenerConfig.SiteId.Equals(current.ID)).DoMany(ctx); e != nil {
				return e
			}
			if _, e := tx.SiteDomain.Delete().Where(query.SiteDomain.SiteId.Equals(current.ID)).DoMany(ctx); e != nil {
				return e
			}
			if _, e := tx.Site.Delete().Where(query.Site.Id.Equals(current.ID)).Do(ctx); e != nil {
				return e
			}
			if _, e := tx.OriginBackend.Delete().Where(query.OriginBackend.OriginPoolId.Equals(site.OriginPoolId)).DoMany(ctx); e != nil {
				return e
			}
			_, e := tx.OriginPool.Delete().Where(query.OriginPool.Id.Equals(site.OriginPoolId)).Do(ctx)
			return e
		})
		if err != nil {
			return err
		}
		return c.NoContent(http.StatusNoContent)
	}
}
