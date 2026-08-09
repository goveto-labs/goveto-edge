package clusters

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/audit"
	"goveto-edge/internal/httpapi/types"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

type addMemberRequest struct {
	UserID     string                  `json:"user_id"`
	Email      string                  `json:"email"`
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

		input.UserID = strings.TrimSpace(input.UserID)
		input.Email = strings.ToLower(strings.TrimSpace(input.Email))
		if input.UserID == "" && input.Email == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "user_id or email is required")
		}
		var user *model.User
		var err error
		if input.UserID != "" {
			user, err = db.User.FindUnique(c.Request().Context(), query.User.Id.Equals(input.UserID))
		} else {
			user, err = db.User.FindUnique(c.Request().Context(), query.User.Email.Equals(input.Email))
		}
		if err != nil {
			return err
		}
		if user == nil {
			return echo.NewHTTPError(http.StatusBadRequest, "user not found")
		}
		if user.Status != model.UserStatusACTIVE {
			return echo.NewHTTPError(http.StatusConflict, "disabled users cannot be added to a cluster")
		}
		input.UserID = user.Id
		clusterID := c.Param("cluster_id")
		cluster, err := db.Cluster.FindUnique(c.Request().Context(), query.Cluster.Id.Equals(clusterID))
		if err != nil {
			return err
		}
		if cluster == nil {
			return echo.NewHTTPError(http.StatusNotFound, "cluster not found")
		}
		if cluster.CreatorId == user.Id {
			return echo.NewHTTPError(http.StatusConflict, "user is already the cluster owner")
		}
		existing, err := db.ClusterMember.Query().Where(
			query.ClusterMember.ClusterId.Equals(clusterID), query.ClusterMember.UserId.Equals(user.Id),
		).Count(c.Request().Context())
		if err != nil {
			return err
		}
		if existing > 0 {
			return echo.NewHTTPError(http.StatusConflict, "user is already a cluster member")
		}

		item, err := db.ClusterMember.Create().
			Set(
				query.ClusterMember.ClusterId.Set(clusterID),
				query.ClusterMember.UserId.Set(input.UserID),
				query.ClusterMember.Permission.Set(input.Permission),
			).
			Do(c.Request().Context())
		if err != nil {
			return err
		}
		item.User = user
		audit.SetResourceID(c, item.ClusterId+":"+item.UserId)
		audit.SetChange(c, nil, types.NewClusterMember(item))
		return types.JSON(c, http.StatusCreated, types.NewClusterMember(item))
	}
}
