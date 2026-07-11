package nodes

import (
	"github.com/labstack/echo/v5"
	nodedomain "goveto-edge/internal/node"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/query"
	"net/http"
)

func deleteNode(db *client.Client, queue *nodedomain.InstallQueue) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := c.Request().Context()
		nodeID := c.Param("node_id")
		node, err := db.Node.FindUnique(ctx, query.Node.Id.Equals(nodeID))
		if err != nil || node.ClusterId != c.Param("cluster_id") {
			return echo.NewHTTPError(http.StatusNotFound, "node not found")
		}
		if err := db.Tx(ctx, func(tx *client.Client) error {
			if _, err := tx.NodeDNSLine.Delete().Where(query.NodeDNSLine.NodeId.Equals(nodeID)).DoMany(ctx); err != nil {
				return err
			}
			if _, err := tx.NodeAddress.Delete().Where(query.NodeAddress.NodeId.Equals(nodeID)).DoMany(ctx); err != nil {
				return err
			}
			_, err := tx.Node.Delete().Where(query.Node.Id.Equals(nodeID)).DoMany(ctx)
			return err
		}); err != nil {
			return err
		}
		_ = queue.Delete(ctx, nodeID)
		return c.NoContent(http.StatusNoContent)
	}
}
