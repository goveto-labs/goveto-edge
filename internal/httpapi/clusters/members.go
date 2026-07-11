package clusters

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/auth"
	"goveto-edge/internal/clusteraccess"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

type addMemberRequest struct {
	UserID     string                  `json:"user_id"`
	Permission model.ClusterPermission `json:"permission"`
}

func addMember(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		_, owner, err := clusteraccess.Check(c.Request().Context(), db, c.Param("cluster_id"), auth.CurrentUID(c))
		if err != nil {
			return err
		}
		if !owner {
			return echo.NewHTTPError(http.StatusForbidden, "only the cluster owner can add members")
		}

		var input addMemberRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		if input.Permission == "" {
			input.Permission = model.ClusterPermissionOPERATOR
		}
		if input.Permission != model.ClusterPermissionOPERATOR {
			return echo.NewHTTPError(http.StatusBadRequest, "only OPERATOR permission can be assigned")
		}

		if _, err := db.User.FindUnique(
			c.Request().Context(),
			query.User.Id.Equals(input.UserID),
		); errors.Is(err, sql.ErrNoRows) {
			return echo.NewHTTPError(http.StatusBadRequest, "user not found")
		} else if err != nil {
			return err
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
		return c.JSON(http.StatusCreated, item)
	}
}
