// Package clusters registers cluster option management endpoints.
package clusters

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"

	authn "goveto-edge/internal/auth"
	"goveto-edge/internal/clusteraccess"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/query"
)

type nameRequest struct {
	Name string `json:"name"`
}

func Register(e *echo.Echo, db *client.Client, sessions *authn.SessionStore) {
	registerSelection(e, db, sessions)
	group := e.Group("/api/v1/clusters/:cluster_id", authn.RequireAuth, clusteraccess.Require(db))
	group.GET("/dns-lines", listDNSLines(db))
	group.GET("/groups", listGroups(db))
	group.POST("/groups", createGroup(db))
	group.GET("/regions", listRegions(db))
	group.POST("/regions", createRegion(db))
	group.POST("/members", addMember(db))
}

// @summary List DNS lines
// @description List DNS lines configured for the cluster.
// @Tags clusters
func listDNSLines(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		items, err := db.DNSLine.Query().
			Where(query.DNSLine.ClusterId.Equals(c.Param("cluster_id"))).
			OrderBy(query.DNSLine.Name.Asc()).
			Do(c.Request().Context())
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, items)
	}
}

// @summary List groups
// @description List node groups in the cluster.
// @Tags clusters
func listGroups(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		items, err := db.ClusterGroup.Query().
			Where(query.ClusterGroup.ClusterId.Equals(c.Param("cluster_id"))).
			OrderBy(query.ClusterGroup.Name.Asc()).
			Do(c.Request().Context())
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, items)
	}
}

// @summary Create group
// @description Create a node group in the cluster.
// @Tags clusters
func createGroup(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input nameRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}

		input.Name = strings.TrimSpace(input.Name)
		if input.Name == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "name is required")
		}

		item, err := db.ClusterGroup.Create().
			Set(
				query.ClusterGroup.ClusterId.Set(c.Param("cluster_id")),
				query.ClusterGroup.Name.Set(input.Name),
			).
			Do(c.Request().Context())
		if err != nil {
			return err
		}
		return c.JSON(http.StatusCreated, item)
	}
}

// @summary List regions
// @description List regions in the cluster.
// @Tags clusters
func listRegions(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		items, err := db.ClusterRegion.Query().
			Where(query.ClusterRegion.ClusterId.Equals(c.Param("cluster_id"))).
			OrderBy(query.ClusterRegion.Name.Asc()).
			Do(c.Request().Context())
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, items)
	}
}

// @summary Create region
// @description Create a region in the cluster.
// @Tags clusters
func createRegion(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input nameRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}

		input.Name = strings.TrimSpace(input.Name)
		if input.Name == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "name is required")
		}

		item, err := db.ClusterRegion.Create().
			Set(
				query.ClusterRegion.ClusterId.Set(c.Param("cluster_id")),
				query.ClusterRegion.Name.Set(input.Name),
			).
			Do(c.Request().Context())
		if err != nil {
			return err
		}
		return c.JSON(http.StatusCreated, item)
	}
}
