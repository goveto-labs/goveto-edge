package nodes

import (
	"context"
	"net"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/audit"
	"goveto-edge/internal/dnssync"
	"goveto-edge/internal/httpapi/types"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

type addAddressRequest struct {
	Address string `json:"address"`
}

// @summary Add node address
// @description Add an IP address to a node.
// @Tags nodes
func addAddress(db *client.Client, dnsService *dnssync.Service) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input addAddressRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}

		input.Address = strings.TrimSpace(input.Address)
		if net.ParseIP(input.Address) == nil {
			return echo.NewHTTPError(http.StatusBadRequest, "address must be a valid IP")
		}

		ctx := c.Request().Context()
		node, err := db.Node.FindUnique(ctx, query.Node.Id.Equals(c.Param("node_id")))
		if err != nil {
			return err
		}
		if node == nil || node.ClusterId != c.Param("cluster_id") {
			return echo.NewHTTPError(http.StatusNotFound, "node not found")
		}

		created, err := db.NodeAddress.Create().
			Set(
				query.NodeAddress.NodeId.Set(node.Id),
				query.NodeAddress.Address.Set(input.Address),
			).
			Do(ctx)
		if err != nil {
			return err
		}
		response := types.NewNodeAddress(created)
		audit.SetChange(c, nil, response)
		if dnsService != nil {
			if _, err := dnsService.EnqueueNodeIPIfChanged(ctx, node.ClusterId); err != nil {
				return err
			}
		}
		return types.JSON(c, http.StatusCreated, response)
	}
}

// @summary Update node address
// @Tags nodes
func updateAddress(db *client.Client, dnsService *dnssync.Service) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input addAddressRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		input.Address = strings.TrimSpace(input.Address)
		if net.ParseIP(input.Address) == nil {
			return echo.NewHTTPError(http.StatusBadRequest, "address must be a valid IP")
		}

		ctx := c.Request().Context()
		node, err := findAddressNode(ctx, db, c.Param("cluster_id"), c.Param("node_id"))
		if err != nil {
			return err
		}
		current, err := db.NodeAddress.FindUnique(ctx, query.NodeAddress.Id.Equals(c.Param("address_id")))
		if err != nil {
			return err
		}
		if current == nil || current.NodeId != node.Id {
			return echo.NewHTTPError(http.StatusNotFound, "address not found")
		}
		updated, err := db.NodeAddress.Update().
			Where(
				query.NodeAddress.Id.Equals(c.Param("address_id")),
				query.NodeAddress.NodeId.Equals(node.Id),
			).
			Set(query.NodeAddress.Address.Set(input.Address)).
			Do(ctx)
		if err != nil {
			return err
		}
		if updated == nil {
			return echo.NewHTTPError(http.StatusNotFound, "address not found")
		}
		response := types.NewNodeAddress(updated)
		audit.SetChange(c, types.NewNodeAddress(current), response)
		if dnsService != nil {
			if _, err := dnsService.EnqueueNodeIPIfChanged(ctx, node.ClusterId); err != nil {
				return err
			}
		}
		return types.JSON(c, http.StatusOK, response)
	}
}

// @summary Delete node address
// @description Delete an address. Nodes are allowed to have no addresses.
// @Tags nodes
func deleteAddress(db *client.Client, dnsService *dnssync.Service) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := c.Request().Context()
		node, err := findAddressNode(ctx, db, c.Param("cluster_id"), c.Param("node_id"))
		if err != nil {
			return err
		}
		deleted, err := db.NodeAddress.Delete().Where(
			query.NodeAddress.Id.Equals(c.Param("address_id")),
			query.NodeAddress.NodeId.Equals(node.Id),
		).Do(ctx)
		if err != nil {
			return err
		}
		if deleted == nil {
			return echo.NewHTTPError(http.StatusNotFound, "address not found")
		}
		audit.SetChange(c, types.NewNodeAddress(deleted), nil)
		if dnsService != nil {
			if _, err := dnsService.EnqueueNodeIPIfChanged(ctx, node.ClusterId); err != nil {
				return err
			}
		}
		return c.NoContent(http.StatusNoContent)
	}
}

func findAddressNode(ctx context.Context, db *client.Client, clusterID, nodeID string) (*model.Node, error) {
	node, err := db.Node.FindUnique(ctx, query.Node.Id.Equals(nodeID))
	if err != nil {
		return nil, err
	}
	if node == nil || node.ClusterId != clusterID {
		return nil, echo.NewHTTPError(http.StatusNotFound, "node not found")
	}
	return node, nil
}
