package nodes

import (
	"net/http"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/httpapi/types"
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
			OrderBy(query.Node.CreatedAt.Desc()).
			Do(c.Request().Context())
		if err != nil {
			return err
		}
		result := make([]types.Node, len(nodes))
		for index := range nodes {
			if err := loadNodeRelations(c.Request().Context(), db, &nodes[index], false); err != nil {
				return err
			}
			result[index] = types.NewNode(&nodes[index])
		}
		return types.JSON(c, http.StatusOK, result)
	}
}

// @summary Get node
// @description Get a single node with addresses, site config versions and cache config.
// @Tags nodes
func get(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		node, err := db.Node.FindUnique(
			c.Request().Context(),
			query.Node.Id.Equals(c.Param("node_id")),
		)
		if err != nil {
			return err
		}
		if node == nil || node.ClusterId != c.Param("cluster_id") {
			return echo.NewHTTPError(http.StatusNotFound, "node not found")
		}
		if err := loadNodeRelations(c.Request().Context(), db, node, true); err != nil {
			return err
		}
		return types.JSON(c, http.StatusOK, types.NewNode(node))
	}
}
