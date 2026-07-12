package nodes

import (
	"context"
	"net/http"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/dnssync"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

type dnsLinesRequest struct {
	DNSLineIDs []string `json:"dns_line_ids"`
}

func updateDNSLines(db *client.Client, dnsService *dnssync.Service) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := c.Request().Context()
		node, err := nodeInCluster(ctx, db, c.Param("cluster_id"), c.Param("node_id"))
		if err != nil {
			return err
		}
		var input dnsLinesRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		seen := make(map[string]struct{}, len(input.DNSLineIDs))
		for _, lineID := range input.DNSLineIDs {
			if _, exists := seen[lineID]; exists {
				return echo.NewHTTPError(http.StatusBadRequest, "duplicate DNS line")
			}
			seen[lineID] = struct{}{}
			line, findErr := db.DNSLine.FindUnique(ctx, query.DNSLine.Id.Equals(lineID))
			if findErr != nil {
				return findErr
			}
			if line == nil || line.ClusterId != node.ClusterId {
				return echo.NewHTTPError(http.StatusBadRequest, "DNS line does not belong to cluster")
			}
		}
		err = db.Tx(ctx, func(tx *client.Client) error {
			if dnsService != nil {
				if err := dnssync.LockClusterTx(ctx, tx, node.ClusterId); err != nil {
					return err
				}
			}
			if _, err := tx.NodeDNSLine.Delete().Where(query.NodeDNSLine.NodeId.Equals(node.Id)).DoMany(ctx); err != nil {
				return err
			}
			for _, lineID := range input.DNSLineIDs {
				if _, err := tx.NodeDNSLine.Create().Set(query.NodeDNSLine.NodeId.Set(node.Id), query.NodeDNSLine.DnsLineId.Set(lineID)).Do(ctx); err != nil {
					return err
				}
			}
			return enqueueDNSIfConfiguredTx(ctx, tx, dnsService, node.ClusterId)
		})
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, map[string]any{"node_id": node.Id, "dns_line_ids": input.DNSLineIDs})
	}
}

func enableNode(db *client.Client, dnsService *dnssync.Service) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := c.Request().Context()
		node, err := nodeInCluster(ctx, db, c.Param("cluster_id"), c.Param("node_id"))
		if err != nil {
			return err
		}
		if node.Status != model.NodeStatusDISABLED {
			return echo.NewHTTPError(http.StatusConflict, "only a disabled node can be enabled")
		}
		err = db.Tx(ctx, func(tx *client.Client) error {
			if dnsService != nil {
				if err := dnssync.LockClusterTx(ctx, tx, node.ClusterId); err != nil {
					return err
				}
			}
			if _, err := tx.Node.Update().Where(query.Node.Id.Equals(node.Id)).Set(query.Node.Status.Set(model.NodeStatusOFFLINE), query.Node.HeartbeatAt.SetNull()).Do(ctx); err != nil {
				return err
			}
			return enqueueDNSIfConfiguredTx(ctx, tx, dnsService, node.ClusterId)
		})
		if err != nil {
			return err
		}
		return c.JSON(http.StatusAccepted, map[string]any{"id": node.Id, "status": model.NodeStatusOFFLINE, "message": "waiting for health check"})
	}
}

func disableNode(db *client.Client, dnsService *dnssync.Service) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := c.Request().Context()
		node, err := nodeInCluster(ctx, db, c.Param("cluster_id"), c.Param("node_id"))
		if err != nil {
			return err
		}
		if node.Status == model.NodeStatusDISABLED {
			return c.JSON(http.StatusOK, map[string]any{"id": node.Id, "status": node.Status})
		}
		if node.Status != model.NodeStatusONLINE && node.Status != model.NodeStatusOFFLINE && node.Status != model.NodeStatusINSTALL_FAILED {
			return echo.NewHTTPError(http.StatusConflict, "node cannot be disabled while installation is pending")
		}
		err = db.Tx(ctx, func(tx *client.Client) error {
			if dnsService != nil {
				if err := dnssync.LockClusterTx(ctx, tx, node.ClusterId); err != nil {
					return err
				}
			}
			if _, err := tx.Node.Update().Where(query.Node.Id.Equals(node.Id)).Set(query.Node.Status.Set(model.NodeStatusDISABLED)).Do(ctx); err != nil {
				return err
			}
			return enqueueDNSIfConfiguredTx(ctx, tx, dnsService, node.ClusterId)
		})
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, map[string]any{"id": node.Id, "status": model.NodeStatusDISABLED})
	}
}

func nodeInCluster(ctx context.Context, db *client.Client, clusterID, nodeID string) (*model.Node, error) {
	node, err := db.Node.FindUnique(ctx, query.Node.Id.Equals(nodeID))
	if err != nil {
		return nil, err
	}
	if node == nil || node.ClusterId != clusterID {
		return nil, echo.NewHTTPError(http.StatusNotFound, "node not found")
	}
	return node, nil
}

func enqueueDNSIfConfiguredTx(ctx context.Context, tx *client.Client, service *dnssync.Service, clusterID string) error {
	if service == nil {
		return nil
	}
	config, err := tx.DNSProviderConfig.FindUnique(ctx, query.DNSProviderConfig.ClusterId.Equals(clusterID))
	if err != nil || config == nil || !config.Enabled {
		return err
	}
	_, err = service.EnqueueTx(ctx, tx, clusterID, nil, model.DNSSyncActionRECONCILE)
	return err
}
