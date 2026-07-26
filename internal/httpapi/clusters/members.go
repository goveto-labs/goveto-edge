package clusters

import (
	"net/http"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/audit"
	"goveto-edge/internal/httpapi/types"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

type addMemberRequest struct {
	UserID     string                  `json:"user_id"`
	Permission model.ClusterPermission `json:"permission"`
}

// @summary Add cluster member
// @description Add a user as a VIEWER or OPERATOR member; requires member management permission.
// @Tags clusters
func addMember(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input addMemberRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		if input.Permission == "" {
			input.Permission = model.ClusterPermissionOPERATOR
		}
		if input.Permission != model.ClusterPermissionOPERATOR && input.Permission != model.ClusterPermissionVIEWER {
			return echo.NewHTTPError(http.StatusBadRequest, "permission must be VIEWER or OPERATOR")
		}

		user, err := db.User.FindUnique(
			c.Request().Context(),
			query.User.Id.Equals(input.UserID),
		)
		if err != nil {
			return err
		}
		if user == nil {
			return echo.NewHTTPError(http.StatusBadRequest, "user not found")
		}

		item, err := db.ClusterMember.Create().
			Set(
				query.ClusterMember.ClusterId.Set(c.Param("cluster_id")),
				query.ClusterMember.UserId.Set(input.UserID),
				query.ClusterMember.Permission.Set(input.Permission),
			).
			Do(c.Request().Context())
		if err != nil {
			return err
		}
		audit.SetResourceID(c, item.ClusterId+":"+item.UserId)
		audit.SetChange(c, nil, types.NewClusterMember(item))
		return types.JSON(c, http.StatusCreated, types.NewClusterMember(item))
	}
}
