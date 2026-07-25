// Package clusteraccess enforces cluster ownership and membership.
package clusteraccess

import (
	"context"
	"net/http"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/auth"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/query"
)

func Require(db *client.Client) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			allowed, _, err := Check(c.Request().Context(), db, c.Param("cluster_id"), auth.CurrentUID(c))
			if err != nil {
				return err
			}
			if !allowed {
				return echo.NewHTTPError(http.StatusForbidden, "cluster access denied")
			}
			return next(c)
		}
	}
}

func RequireOwner(db *client.Client) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			_, owner, err := Check(c.Request().Context(), db, c.Param("cluster_id"), auth.CurrentUID(c))
			if err != nil {
				return err
			}
			if !owner {
				return echo.NewHTTPError(http.StatusForbidden, "cluster owner access required")
			}
			return next(c)
		}
	}
}

func Check(ctx context.Context, db *client.Client, clusterID, uid string) (allowed, owner bool, err error) {
	cluster, err := db.Cluster.FindUnique(ctx, query.Cluster.Id.Equals(clusterID))
	if err != nil {
		return false, false, err
	}
	if cluster == nil {
		return false, false, nil
	}
	if cluster.CreatorId == uid {
		return true, true, nil
	}
	member, err := db.ClusterMember.Query().Where(query.ClusterMember.ClusterId.Equals(clusterID), query.ClusterMember.UserId.Equals(uid)).First(ctx)
	if err != nil {
		return false, false, err
	}
	if member == nil {
		return false, false, nil
	}
	return true, false, nil
}
