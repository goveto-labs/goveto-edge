package nodes

import (
	"net/http"

	"github.com/labstack/echo/v5"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/query"
)

// @summary List nodes
// @description List nodes in the cluster with addresses and site config versions.
// @Tags nodes
func list(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		nodes, err := db.Node.Query().
			Where(query.Node.ClusterId.Equals(c.Param("cluster_id"))).
			Include(
				query.Node.Addresses.Fetch(),
				query.Node.SiteConfigVersions.Fetch(),
			).
			OrderBy(query.Node.CreatedAt.Desc()).
			Do(c.Request().Context())
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, nodes)
	}
}

// @summary Get node
// @description Get a single node with addresses, site config versions and cache config.
// @Tags nodes
func get(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		node, err := db.Node.Query().
			Where(query.Node.Id.Equals(c.Param("node_id"))).
			Include(
				query.Node.Addresses.Fetch(),
				query.Node.SiteConfigVersions.Fetch(),
				query.Node.CacheConfig.Fetch(),
			).
			First(c.Request().Context())
		if err != nil || node.ClusterId != c.Param("cluster_id") {
			return echo.NewHTTPError(http.StatusNotFound, "node not found")
		}
		return c.JSON(http.StatusOK, node)
	}
}
