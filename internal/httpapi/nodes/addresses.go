package nodes

import (
	"net"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/dnssync"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

type addAddressRequest struct {
	Address string `json:"address"`
	Primary bool   `json:"primary"`
}

// @summary Add node address
// @description Add an IP address to a node; optionally mark it as primary.
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

		var created any
		err = db.Tx(ctx, func(tx *client.Client) error {
			if input.Primary && dnsService != nil {
				if err := dnssync.LockClusterTx(ctx, tx, node.ClusterId); err != nil {
					return err
				}
			}
			if input.Primary {
				if _, err := tx.NodeAddress.Update().
					Where(query.NodeAddress.NodeId.Equals(node.Id)).
					Set(query.NodeAddress.Primary.Set(false)).
					DoMany(ctx); err != nil {
					return err
				}
			}

			item, err := tx.NodeAddress.Create().
				Set(
					query.NodeAddress.NodeId.Set(node.Id),
					query.NodeAddress.Address.Set(input.Address),
					query.NodeAddress.Primary.Set(input.Primary),
				).
				Do(ctx)
			created = item
			if err != nil || !input.Primary || dnsService == nil {
				return err
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
		})
		if err != nil {
			return err
		}
		return c.JSON(http.StatusCreated, created)
	}
}
