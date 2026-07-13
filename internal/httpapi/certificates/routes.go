// Package certificates registers cluster certificate endpoints.
package certificates

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/auth"
	"goveto-edge/internal/clusteraccess"
	"goveto-edge/internal/httpapi/types"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/query"
)

type uploadRequest struct {
	Name        string `json:"name"`
	Certificate string `json:"certificate"`
	PrivateKey  string `json:"private_key"`
}

func Register(e *echo.Echo, db *client.Client) {
	group := e.Group("/api/v1/clusters/:cluster_id/certificates", auth.RequireAuth, clusteraccess.Require(db))
	group.GET("", list(db))
	group.POST("", upload(db))
}

// @summary List certificates
// @description List TLS certificates in the cluster (private keys omitted).
// @Tags certificates
func list(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		items, err := db.Certificate.Query().
			Where(query.Certificate.ClusterId.Equals(c.Param("cluster_id"))).
			OrderBy(query.Certificate.Name.Asc()).
			Do(c.Request().Context())
		if err != nil {
			return err
		}
		for index := range items {
			items[index].PrivateKeyPem = ""
		}
		return types.JSON(c, http.StatusOK, items)
	}
}

// @summary Upload certificate
// @description Upload a certificate and private key pair for the cluster.
// @Tags certificates
func upload(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input uploadRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}

		input.Name = strings.TrimSpace(input.Name)
		if input.Name == "" || input.Certificate == "" || input.PrivateKey == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "name, certificate and private_key are required")
		}

		pair, err := tls.X509KeyPair([]byte(input.Certificate), []byte(input.PrivateKey))
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "certificate and private key do not match")
		}

		leaf, err := x509.ParseCertificate(pair.Certificate[0])
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid certificate")
		}

		fingerprint := sha256.Sum256(leaf.Raw)
		item, err := db.Certificate.Create().
			Set(
				query.Certificate.ClusterId.Set(c.Param("cluster_id")),
				query.Certificate.Name.Set(input.Name),
				query.Certificate.CertPem.Set(input.Certificate),
				query.Certificate.PrivateKeyPem.Set(input.PrivateKey),
				query.Certificate.Fingerprint.Set(hex.EncodeToString(fingerprint[:])),
				query.Certificate.ExpiresAt.Set(leaf.NotAfter),
			).
			Do(c.Request().Context())
		if err != nil {
			return err
		}

		item.PrivateKeyPem = ""
		return types.JSON(c, http.StatusCreated, item)
	}
}
