package nodes

import (
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/query"
	"net"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"
)

type addAddressRequest struct {
	Address string `json:"address"`
	Primary bool   `json:"primary"`
}

func addAddress(db *client.Client) echo.HandlerFunc {
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
		if err != nil || node.ClusterId != c.Param("cluster_id") {
			return echo.NewHTTPError(http.StatusNotFound, "node not found")
		}
		var created any
		err = db.Tx(ctx, func(tx *client.Client) error {
			if input.Primary {
				if _, err := tx.NodeAddress.Update().Where(query.NodeAddress.NodeId.Equals(node.Id)).Set(query.NodeAddress.Primary.Set(false)).DoMany(ctx); err != nil {
					return err
				}
			}
			item, err := tx.NodeAddress.Create().Set(query.NodeAddress.NodeId.Set(node.Id), query.NodeAddress.Address.Set(input.Address), query.NodeAddress.Primary.Set(input.Primary)).Do(ctx)
			created = item
			return err
		})
		if err != nil {
			return err
		}
		return c.JSON(http.StatusCreated, created)
	}
}
