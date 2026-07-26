// Package certificates registers cluster certificate lifecycle endpoints.
package certificates

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"

	"goveto-edge/internal/audit"
	"goveto-edge/internal/auth"
	"goveto-edge/internal/certmanager"
	"goveto-edge/internal/clusteraccess"
	"goveto-edge/internal/httpapi/types"
	"goveto-edge/internal/rbac"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

type uploadRequest struct {
	Name        string `json:"name"`
	Certificate string `json:"certificate"`
	PrivateKey  string `json:"private_key"`
}

type acmeRequest struct {
	Name            string                  `json:"name"`
	Domains         []string                `json:"domains"`
	Email           string                  `json:"email"`
	DirectoryURL    string                  `json:"directory_url"`
	ChallengeType   model.ACMEChallengeType `json:"challenge_type"`
	AutoRenew       *bool                   `json:"auto_renew"`
	RenewBeforeDays int                     `json:"renew_before_days"`
}

type updateRequest struct {
	Name            *string `json:"name"`
	AutoRenew       *bool   `json:"auto_renew"`
	RenewBeforeDays *int    `json:"renew_before_days"`
}

func Register(e *echo.Echo, db *client.Client, service *certmanager.Service) {
	group := e.Group("/api/v1/clusters/:cluster_id/certificates", auth.RequireAuth)
	read := clusteraccess.RequirePermission(db, rbac.PermissionClusterRead)
	manage := clusteraccess.RequirePermission(db, rbac.PermissionCertificateManage)
	group.GET("", list(db), read)
	group.GET("/:certificate_id", get(db), read)
	group.POST("", upload(db, service), manage)
	group.POST("/acme", issueACME(db, service), manage)
	group.PATCH("/:certificate_id", update(db), manage)
	group.PUT("/:certificate_id/material", replaceMaterial(db, service), manage)
	group.DELETE("/:certificate_id", remove(db, service), clusteraccess.RequirePermission(db, rbac.PermissionCertificateDelete))
	group.POST("/:certificate_id/renew", enqueueOperation(db, service, model.CertificateOperationRENEW), manage)
	group.POST("/:certificate_id/reissue", enqueueOperation(db, service, model.CertificateOperationREISSUE), manage)
	group.POST("/:certificate_id/publish", enqueueOperation(db, service, model.CertificateOperationREPUBLISH), manage)
	group.GET("/:certificate_id/jobs", listJobs(db), read)
	e.GET("/.well-known/acme-challenge/:token", serveChallenge(service))
}

// @summary List certificates
// @description List TLS certificates and lifecycle state in the cluster (private keys omitted).
// @Tags certificates
func list(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		items, err := db.Certificate.Query().Where(query.Certificate.ClusterId.Equals(c.Param("cluster_id"))).OrderBy(query.Certificate.Name.Asc()).Do(c.Request().Context())
		if err != nil {
			return err
		}
		result := make([]types.Certificate, len(items))
		for index := range items {
			result[index] = types.NewCertificate(&items[index])
		}
		return types.JSON(c, http.StatusOK, result)
	}
}

func get(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		item, err := ownedCertificate(c, db)
		if err != nil {
			return err
		}
		return types.JSON(c, http.StatusOK, types.NewCertificate(item))
	}
}

// @summary Upload certificate
// @description Validate and envelope-encrypt a manually managed certificate and private key.
// @Tags certificates
func upload(db *client.Client, service *certmanager.Service) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input uploadRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		input.Name = strings.TrimSpace(input.Name)
		if input.Name == "" || input.Certificate == "" || input.PrivateKey == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "name, certificate and private_key are required")
		}
		material, err := certmanager.ValidateMaterial(input.Certificate, input.PrivateKey, time.Now().UTC())
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		id := uuid.NewString()
		item, err := db.Certificate.Create().Set(
			query.Certificate.Id.Set(id), query.Certificate.ClusterId.Set(c.Param("cluster_id")), query.Certificate.Name.Set(input.Name),
			query.Certificate.Source.Set(model.CertificateSourceMANUAL), query.Certificate.Status.Set(model.CertificateStatusPENDING),
			query.Certificate.DomainsJson.Set(certmanager.EncodeDomains(material.Domains)),
		).Do(c.Request().Context())
		if err != nil {
			return err
		}
		if err = service.StoreManual(c.Request().Context(), item, material); err != nil {
			return err
		}
		item, err = db.Certificate.FindUnique(c.Request().Context(), query.Certificate.Id.Equals(id))
		if err != nil {
			return err
		}
		response := types.NewCertificate(item)
		audit.SetResourceID(c, item.Id)
		audit.SetChange(c, nil, response)
		return types.JSON(c, http.StatusCreated, response)
	}
}

func issueACME(db *client.Client, service *certmanager.Service) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input acmeRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		input.Name, input.Email = strings.TrimSpace(input.Name), strings.TrimSpace(input.Email)
		if input.Name == "" || input.Email == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "name and email are required")
		}
		domains, err := certmanager.NormalizeDomains(input.Domains, true)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		if input.ChallengeType == "" {
			input.ChallengeType = model.ACMEChallengeTypeHTTP_01
		}
		if input.ChallengeType != model.ACMEChallengeTypeHTTP_01 && input.ChallengeType != model.ACMEChallengeTypeDNS_01 {
			return echo.NewHTTPError(http.StatusBadRequest, "challenge_type must be HTTP_01 or DNS_01")
		}
		for _, domain := range domains {
			if strings.HasPrefix(domain, "*.") && input.ChallengeType != model.ACMEChallengeTypeDNS_01 {
				return echo.NewHTTPError(http.StatusBadRequest, "wildcard certificates require DNS_01")
			}
		}
		directory := strings.TrimSpace(input.DirectoryURL)
		if directory != "" {
			parsed, parseErr := url.Parse(directory)
			if parseErr != nil || parsed.Scheme != "https" || parsed.Host == "" {
				return echo.NewHTTPError(http.StatusBadRequest, "directory_url must be an HTTPS URL")
			}
		}
		if input.RenewBeforeDays == 0 {
			input.RenewBeforeDays = 30
		}
		if input.RenewBeforeDays < 1 || input.RenewBeforeDays > 90 {
			return echo.NewHTTPError(http.StatusBadRequest, "renew_before_days must be between 1 and 90")
		}
		autoRenew := true
		if input.AutoRenew != nil {
			autoRenew = *input.AutoRenew
		}
		sets := []query.CertificateSetClause{
			query.Certificate.ClusterId.Set(c.Param("cluster_id")), query.Certificate.Name.Set(input.Name),
			query.Certificate.Source.Set(model.CertificateSourceACME), query.Certificate.Status.Set(model.CertificateStatusPENDING),
			query.Certificate.DomainsJson.Set(certmanager.EncodeDomains(domains)), query.Certificate.AcmeEmail.Set(input.Email),
			query.Certificate.AcmeChallengeType.Set(input.ChallengeType), query.Certificate.AutoRenew.Set(autoRenew),
			query.Certificate.RenewBeforeDays.Set(input.RenewBeforeDays),
		}
		if directory != "" {
			sets = append(sets, query.Certificate.AcmeDirectoryUrl.Set(directory))
		}
		item, err := db.Certificate.Create().Set(sets...).Do(c.Request().Context())
		if err != nil {
			return err
		}
		job, err := service.Enqueue(c.Request().Context(), item.Id, model.CertificateOperationISSUE)
		if err != nil {
			return err
		}
		response := map[string]any{"certificate": types.NewCertificate(item), "job": types.NewCertificateJob(job)}
		audit.SetResourceID(c, item.Id)
		audit.SetChange(c, nil, response)
		return types.JSON(c, http.StatusAccepted, response)
	}
}

func update(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		item, err := ownedCertificate(c, db)
		if err != nil {
			return err
		}
		before := types.NewCertificate(item)
		var input updateRequest
		if err = c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		sets := make([]query.CertificateSetClause, 0, 3)
		if input.Name != nil {
			name := strings.TrimSpace(*input.Name)
			if name == "" {
				return echo.NewHTTPError(http.StatusBadRequest, "name is required")
			}
			sets = append(sets, query.Certificate.Name.Set(name))
		}
		if input.AutoRenew != nil {
			if item.Source != model.CertificateSourceACME {
				return echo.NewHTTPError(http.StatusBadRequest, "auto renewal is only available for ACME certificates")
			}
			sets = append(sets, query.Certificate.AutoRenew.Set(*input.AutoRenew))
		}
		if input.RenewBeforeDays != nil {
			if *input.RenewBeforeDays < 1 || *input.RenewBeforeDays > 90 {
				return echo.NewHTTPError(http.StatusBadRequest, "renew_before_days must be between 1 and 90")
			}
			sets = append(sets, query.Certificate.RenewBeforeDays.Set(*input.RenewBeforeDays))
		}
		if len(sets) == 0 {
			return types.JSON(c, http.StatusOK, types.NewCertificate(item))
		}
		item, err = db.Certificate.Update().Where(query.Certificate.Id.Equals(item.Id)).Set(sets...).Do(c.Request().Context())
		if err != nil {
			return err
		}
		response := types.NewCertificate(item)
		audit.SetChange(c, before, response)
		return types.JSON(c, http.StatusOK, response)
	}
}

func replaceMaterial(db *client.Client, service *certmanager.Service) echo.HandlerFunc {
	return func(c *echo.Context) error {
		item, err := ownedCertificate(c, db)
		if err != nil {
			return err
		}
		before := types.NewCertificate(item)
		var input uploadRequest
		if err = c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		material, err := certmanager.ValidateMaterial(input.Certificate, input.PrivateKey, time.Now().UTC())
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		if err = service.StoreManual(c.Request().Context(), item, material); err != nil {
			return err
		}
		item, err = db.Certificate.FindUnique(c.Request().Context(), query.Certificate.Id.Equals(item.Id))
		if err != nil {
			return err
		}
		response := types.NewCertificate(item)
		audit.SetChange(c, before, response)
		return types.JSON(c, http.StatusOK, response)
	}
}

func remove(db *client.Client, service *certmanager.Service) echo.HandlerFunc {
	return func(c *echo.Context) error {
		item, err := ownedCertificate(c, db)
		if err != nil {
			return err
		}
		if err = service.Delete(c.Request().Context(), item.Id); err != nil {
			return err
		}
		audit.SetChange(c, types.NewCertificate(item), nil)
		return c.NoContent(http.StatusNoContent)
	}
}

func enqueueOperation(db *client.Client, service *certmanager.Service, operation model.CertificateOperation) echo.HandlerFunc {
	return func(c *echo.Context) error {
		item, err := ownedCertificate(c, db)
		if err != nil {
			return err
		}
		if (operation == model.CertificateOperationRENEW || operation == model.CertificateOperationREISSUE) && item.Source != model.CertificateSourceACME {
			return echo.NewHTTPError(http.StatusBadRequest, "operation requires an ACME certificate")
		}
		job, err := service.Enqueue(c.Request().Context(), item.Id, operation)
		if err != nil {
			return err
		}
		response := types.NewCertificateJob(job)
		audit.SetChange(c, types.NewCertificate(item), response)
		return types.JSON(c, http.StatusAccepted, response)
	}
}

func listJobs(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		item, err := ownedCertificate(c, db)
		if err != nil {
			return err
		}
		jobs, err := db.CertificateJob.Query().Where(query.CertificateJob.CertificateId.Equals(item.Id)).OrderBy(query.CertificateJob.CreatedAt.Desc()).Do(c.Request().Context())
		if err != nil {
			return err
		}
		result := make([]types.CertificateJob, len(jobs))
		for index := range jobs {
			result[index] = types.NewCertificateJob(&jobs[index])
		}
		return types.JSON(c, http.StatusOK, result)
	}
}

func serveChallenge(service *certmanager.Service) echo.HandlerFunc {
	return func(c *echo.Context) error {
		value, ok, err := service.HTTPChallenge(c.Request().Context(), c.Param("token"))
		if err != nil {
			return err
		}
		if !ok {
			return echo.NewHTTPError(http.StatusNotFound, "challenge not found")
		}
		return c.String(http.StatusOK, value)
	}
}

func ownedCertificate(c *echo.Context, db *client.Client) (*model.Certificate, error) {
	item, err := db.Certificate.FindUnique(c.Request().Context(), query.Certificate.Id.Equals(c.Param("certificate_id")))
	if err != nil {
		return nil, err
	}
	if item == nil || item.ClusterId != c.Param("cluster_id") {
		return nil, echo.NewHTTPError(http.StatusNotFound, "certificate not found")
	}
	return item, nil
}
