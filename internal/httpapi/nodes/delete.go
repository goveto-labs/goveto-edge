package nodes

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/dnssync"
	"goveto-edge/internal/httpapi/types"
	nodedomain "goveto-edge/internal/node"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

// @summary Delete node
// @description Delete a node and related credentials, addresses and config records.
// @Tags nodes
func deleteNode(db *client.Client, queue *nodedomain.InstallQueue, dnsService *dnssync.Service) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := c.Request().Context()
		nodeID := c.Param("node_id")
		node, err := db.Node.FindUnique(ctx, query.Node.Id.Equals(nodeID))
		if err != nil {
			return err
		}
		if node == nil || node.ClusterId != c.Param("cluster_id") {
			return echo.NewHTTPError(http.StatusNotFound, "node not found")
		}
		if err := db.Tx(ctx, func(tx *client.Client) error {
			if dnsService != nil {
				if err := dnssync.LockClusterTx(ctx, tx, node.ClusterId); err != nil {
					return err
				}
			}
			if _, err := tx.DNSManagedRecord.Update().
				Where(query.DNSManagedRecord.NodeId.Equals(&nodeID)).
				Set(
					query.DNSManagedRecord.NodeId.SetNull(),
					query.DNSManagedRecord.UpdatedAt.Set(time.Now()),
				).
				DoMany(ctx); err != nil {
				return err
			}
			if _, err := tx.NodeSiteConfigVersion.Delete().Where(query.NodeSiteConfigVersion.NodeId.Equals(nodeID)).DoMany(ctx); err != nil {
				return err
			}
			if _, err := tx.NodeCredential.Delete().Where(query.NodeCredential.NodeId.Equals(nodeID)).DoMany(ctx); err != nil {
				return err
			}
			if _, err := tx.NodeCacheConfig.Delete().Where(query.NodeCacheConfig.NodeId.Equals(nodeID)).DoMany(ctx); err != nil {
				return err
			}
			if _, err := tx.NodeDNSLine.Delete().Where(query.NodeDNSLine.NodeId.Equals(nodeID)).DoMany(ctx); err != nil {
				return err
			}
			if _, err := tx.NodeAddress.Delete().Where(query.NodeAddress.NodeId.Equals(nodeID)).DoMany(ctx); err != nil {
				return err
			}
			if _, err := tx.Node.Delete().Where(query.Node.Id.Equals(nodeID)).DoMany(ctx); err != nil {
				return err
			}
			if dnsService == nil {
				return nil
			}
			config, err := tx.DNSProviderConfig.FindUnique(
				ctx,
				query.DNSProviderConfig.ClusterId.Equals(node.ClusterId),
			)
			if err != nil || config == nil || !config.Enabled {
				return err
			}
			_, err = dnsService.EnqueueTx(
				ctx,
				tx,
				node.ClusterId,
				nil,
				model.DNSSyncActionRECONCILE,
			)
			return err
		}); err != nil {
			return err
		}
		_ = queue.Delete(ctx, nodeID)
		return types.JSON(c, http.StatusOK, nil)
	}
}
