package nodes

import (
	"context"
	"net/http"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/dnssync"
	"goveto-edge/internal/edgecontrol"
	"goveto-edge/internal/httpapi/types"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

type dnsLinesRequest struct {
	DNSLineIDs []string `json:"dns_line_ids"`
}
type dnsLinesResponse struct {
	NodeID     string   `json:"node_id"`
	DNSLineIDs []string `json:"dns_line_ids"`
}
type nodeStatusResponse struct {
	ID      string           `json:"id"`
	Status  model.NodeStatus `json:"status"`
	Message string           `json:"message,omitempty"`
}

// @summary Update node DNS lines
// @description Replace the DNS lines assigned to a node and enqueue DNS reconciliation.
// @Tags nodes
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
			if _, err := tx.NodeDNSLine.Delete().Where(query.NodeDNSLine.NodeId.Equals(node.Id)).DoMany(ctx); err != nil {
				return err
			}
			for _, lineID := range input.DNSLineIDs {
				if _, err := tx.NodeDNSLine.Create().Set(query.NodeDNSLine.NodeId.Set(node.Id), query.NodeDNSLine.DnsLineId.Set(lineID)).Do(ctx); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		if err := enqueueDNSIfChanged(ctx, dnsService, node.ClusterId); err != nil {
			return err
		}
		return types.JSON(c, http.StatusOK, dnsLinesResponse{NodeID: node.Id, DNSLineIDs: input.DNSLineIDs})
	}
}

// @summary Enable node
// @description Re-enable a disabled node and wait for its management channel.
// @Tags nodes
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
			if _, err := tx.Node.Update().Where(query.Node.Id.Equals(node.Id)).Set(
				query.Node.Status.Set(model.NodeStatusOFFLINE),
				query.Node.HeartbeatAt.SetNull(),
				query.Node.InstallError.SetNull(),
			).Do(ctx); err != nil {
				return err
			}
			return nil
		})
		if err != nil {
			return err
		}
		if err := enqueueDNSIfChanged(ctx, dnsService, node.ClusterId); err != nil {
			return err
		}
		return types.JSON(c, http.StatusAccepted, nodeStatusResponse{ID: node.Id, Status: model.NodeStatusOFFLINE, Message: "waiting for the agent management channel"})
	}
}

// @summary Disable node
// @description Disable a node and remove it from DNS scheduling.
// @Tags nodes
func disableNode(db *client.Client, gateway *edgecontrol.Gateway, dnsService *dnssync.Service) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := c.Request().Context()
		node, err := nodeInCluster(ctx, db, c.Param("cluster_id"), c.Param("node_id"))
		if err != nil {
			return err
		}
		if node.Status == model.NodeStatusDISABLED {
			if _, err := db.RawExec(ctx, `UPDATE agent_tasks SET status = 'CANCELLED',
				error = 'node is disabled', lease_owner = NULL, lease_until = NULL, updated_at = NOW()
				WHERE node_id = $1 AND status IN ('PENDING', 'RUNNING')`, node.Id); err != nil {
				return err
			}
			if gateway != nil {
				gateway.Disconnect(ctx, node.Id)
			}
			return types.JSON(c, http.StatusOK, nodeStatusResponse{ID: node.Id, Status: node.Status})
		}
		if node.Status != model.NodeStatusONLINE && node.Status != model.NodeStatusOFFLINE && node.Status != model.NodeStatusINSTALL_FAILED {
			return echo.NewHTTPError(http.StatusConflict, "node cannot be disabled while installation is pending")
		}
		err = db.Tx(ctx, func(tx *client.Client) error {
			if _, err := tx.Node.Update().Where(query.Node.Id.Equals(node.Id)).Set(query.Node.Status.Set(model.NodeStatusDISABLED)).Do(ctx); err != nil {
				return err
			}
			_, err := tx.RawExec(ctx, `UPDATE agent_tasks SET status = 'CANCELLED',
				error = 'node was disabled', lease_owner = NULL, lease_until = NULL, updated_at = NOW()
				WHERE node_id = $1 AND status IN ('PENDING', 'RUNNING')`, node.Id)
			return err
		})
		if err != nil {
			return err
		}
		if gateway != nil {
			gateway.Disconnect(ctx, node.Id)
		}
		if err := enqueueDNSIfChanged(ctx, dnsService, node.ClusterId); err != nil {
			return err
		}
		return types.JSON(c, http.StatusOK, nodeStatusResponse{ID: node.Id, Status: model.NodeStatusDISABLED})
	}
}

// @summary Revoke node management credential
// @description Revoke the active mTLS certificate and disconnect the management channel. Reinstallation is required to admit the node again.
// @Tags nodes
func revokeNodeCredential(db *client.Client, gateway *edgecontrol.Gateway, dnsService *dnssync.Service) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := c.Request().Context()
		node, err := nodeInCluster(ctx, db, c.Param("cluster_id"), c.Param("node_id"))
		if err != nil {
			return err
		}
		if err := gateway.Revoke(ctx, node.Id); err != nil {
			return err
		}
		if _, err := db.Node.Update().Where(query.Node.Id.Equals(node.Id)).Set(
			query.Node.Status.Set(model.NodeStatusOFFLINE),
			query.Node.HeartbeatAt.SetNull(),
		).Do(ctx); err != nil {
			return err
		}
		if err := enqueueDNSIfChanged(ctx, dnsService, node.ClusterId); err != nil {
			return err
		}
		return types.JSON(c, http.StatusOK, nodeStatusResponse{
			ID: node.Id, Status: model.NodeStatusOFFLINE, Message: "management credential revoked; reinstall the node to reconnect",
		})
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

func enqueueDNSIfChanged(ctx context.Context, service *dnssync.Service, clusterID string) error {
	if service == nil {
		return nil
	}
	_, err := service.EnqueueNodeIPIfChanged(ctx, clusterID)
	return err
}
