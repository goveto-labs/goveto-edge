package clusters

import (
	"context"
	"net/http"
	"time"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/audit"
	authn "goveto-edge/internal/auth"
	"goveto-edge/internal/clusteraccess"
	"goveto-edge/internal/httpapi/types"
	"goveto-edge/internal/rbac"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

// registerMemberManagement mounts the member mutation endpoints. The earlier
// add-member route is retained; these complete the management surface
// (list / change role / remove / transfer ownership) so the cluster MEMBER
// role and the cluster.member.manage RBAC permission are exercised end-to-end.
func registerMemberManagement(group *echo.Group, db *client.Client) {
	group.GET("/members", listMembers(db))
	group.PUT("/members/:user_id", updateMember(db), clusteraccess.RequirePermission(db, rbac.PermissionMemberManage))
	group.DELETE("/members/:user_id", removeMember(db), clusteraccess.RequirePermission(db, rbac.PermissionMemberManage))
}

type updateMemberRequest struct {
	Permission model.ClusterPermission `json:"permission"`
}

// @summary List cluster members
// @description List the cluster creator as OWNER together with all membership rows and their roles.
// @Tags clusters
func listMembers(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		clusterID := c.Param("cluster_id")
		cluster, err := db.Cluster.FindUnique(c.Request().Context(), query.Cluster.Id.Equals(clusterID))
		if err != nil {
			return err
		}
		if cluster == nil {
			return echo.NewHTTPError(http.StatusNotFound, "cluster not found")
		}
		members, err := db.ClusterMember.Query().
			Where(query.ClusterMember.ClusterId.Equals(clusterID)).
			OrderBy(query.ClusterMember.CreatedAt.Asc()).
			Do(c.Request().Context())
		if err != nil {
			return err
		}
		result := memberResources(cluster, members)
		return types.JSON(c, http.StatusOK, result)
	}
}

func memberResources(cluster *model.Cluster, members []model.ClusterMember) []types.ClusterMember {
	result := make([]types.ClusterMember, 0, len(members)+1)
	result = append(result, types.ClusterMember{
		ClusterID: cluster.Id, UserID: cluster.CreatorId,
		Permission: model.ClusterPermissionOWNER, CreatedAt: cluster.CreatedAt,
	})
	for index := range members {
		if members[index].UserId != cluster.CreatorId {
			result = append(result, types.NewClusterMember(&members[index]))
		}
	}
	return result
}

// @summary Update cluster member role
// @description Change a member's role to VIEWER or OPERATOR, or transfer ownership with OWNER; transfer makes the previous creator an OPERATOR and requires cluster.transfer.
// @Tags clusters
func updateMember(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		clusterID := c.Param("cluster_id")
		targetUserID := c.Param("user_id")
		var input updateMemberRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		if input.Permission != model.ClusterPermissionVIEWER &&
			input.Permission != model.ClusterPermissionOPERATOR &&
			input.Permission != model.ClusterPermissionOWNER {
			return echo.NewHTTPError(http.StatusBadRequest, "permission must be VIEWER, OPERATOR or OWNER")
		}

		before, member, err := loadMember(c, db, clusterID, targetUserID)
		if err != nil {
			return err
		}
		if member == nil {
			return echo.NewHTTPError(http.StatusNotFound, "member not found")
		}
		// Promoting to OWNER is a cluster ownership transfer: the actor must
		// additionally hold cluster.transfer. Ordinary role changes only need
		// cluster.member.manage (enforced by the route middleware).
		if input.Permission == model.ClusterPermissionOWNER {
			transfer, err := transferOwnership(c.Request().Context(), db, clusterID, targetUserID, authn.CurrentUID(c))
			if err != nil {
				return err
			}
			audit.SetResourceID(c, clusterID+":"+targetUserID)
			audit.SetChange(c,
				map[string]any{"creator_id": transfer.PreviousCreatorID, "member": transfer.BeforeMember},
				map[string]any{"creator_id": targetUserID, "member": transfer.Member, "previous_creator_permission": model.ClusterPermissionOPERATOR},
			)
			return types.JSON(c, http.StatusOK, transfer.Member)
		}

		if member.Permission == input.Permission {
			return types.JSON(c, http.StatusOK, types.NewClusterMember(member))
		}
		updated, err := db.ClusterMember.Update().
			Where(query.ClusterMember.ClusterId.Equals(clusterID), query.ClusterMember.UserId.Equals(targetUserID)).
			Set(query.ClusterMember.Permission.Set(input.Permission)).
			Do(c.Request().Context())
		if err != nil {
			return err
		}
		audit.SetResourceID(c, clusterID+":"+targetUserID)
		audit.SetChange(c, types.NewClusterMember(before), types.NewClusterMember(updated))
		return types.JSON(c, http.StatusOK, types.NewClusterMember(updated))
	}
}

type ownershipTransferResult struct {
	Member            types.ClusterMember
	BeforeMember      types.ClusterMember
	PreviousCreatorID string
}

type clusterLockRow struct {
	ID string `db:"id"`
}

func transferOwnership(ctx context.Context, db *client.Client, clusterID, targetUserID, actorID string) (ownershipTransferResult, error) {
	var result ownershipTransferResult
	err := db.Tx(ctx, func(tx *client.Client) error {
		locked, err := client.Raw[clusterLockRow](ctx, tx, "SELECT id FROM clusters WHERE id = $1 FOR UPDATE", clusterID)
		if err != nil {
			return err
		}
		if len(locked) == 0 {
			return echo.NewHTTPError(http.StatusNotFound, "cluster not found")
		}
		allowed, _, err := clusteraccess.Authorize(ctx, tx, clusterID, actorID, rbac.PermissionClusterTransfer)
		if err != nil {
			return err
		}
		if !allowed {
			return echo.NewHTTPError(http.StatusForbidden, "permission denied: cluster.transfer")
		}
		cluster, err := tx.Cluster.FindUnique(ctx, query.Cluster.Id.Equals(clusterID))
		if err != nil {
			return err
		}
		if cluster == nil {
			return echo.NewHTTPError(http.StatusNotFound, "cluster not found")
		}
		target, err := findMember(ctx, tx, clusterID, targetUserID)
		if err != nil {
			return err
		}
		if target == nil {
			return echo.NewHTTPError(http.StatusNotFound, "member not found")
		}

		result.PreviousCreatorID = cluster.CreatorId
		result.BeforeMember = types.NewClusterMember(target)
		if _, err = tx.Cluster.Update().
			Where(query.Cluster.Id.Equals(clusterID)).
			Set(query.Cluster.CreatorId.Set(targetUserID), query.Cluster.UpdatedAt.Set(time.Now())).
			Do(ctx); err != nil {
			return err
		}
		if _, err = tx.ClusterMember.Delete().
			Where(query.ClusterMember.ClusterId.Equals(clusterID), query.ClusterMember.UserId.Equals(targetUserID)).
			Do(ctx); err != nil {
			return err
		}

		previous, err := findMember(ctx, tx, clusterID, result.PreviousCreatorID)
		if err != nil {
			return err
		}
		if previous == nil {
			if _, err = tx.ClusterMember.Create().Set(
				query.ClusterMember.ClusterId.Set(clusterID),
				query.ClusterMember.UserId.Set(result.PreviousCreatorID),
				query.ClusterMember.Permission.Set(model.ClusterPermissionOPERATOR),
			).Do(ctx); err != nil {
				return err
			}
		} else if previous.Permission != model.ClusterPermissionOPERATOR {
			if _, err = tx.ClusterMember.Update().
				Where(query.ClusterMember.ClusterId.Equals(clusterID), query.ClusterMember.UserId.Equals(result.PreviousCreatorID)).
				Set(query.ClusterMember.Permission.Set(model.ClusterPermissionOPERATOR)).
				Do(ctx); err != nil {
				return err
			}
		}

		result.Member = types.ClusterMember{
			ClusterID: clusterID, UserID: targetUserID,
			Permission: model.ClusterPermissionOWNER, CreatedAt: cluster.CreatedAt,
		}
		return nil
	})
	return result, err
}

// @summary Remove cluster member
// @description Remove a member from the cluster; requires member management permission.
// @Tags clusters
func removeMember(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		clusterID := c.Param("cluster_id")
		targetUserID := c.Param("user_id")
		cluster, err := db.Cluster.FindUnique(c.Request().Context(), query.Cluster.Id.Equals(clusterID))
		if err != nil {
			return err
		}
		if cluster != nil && cluster.CreatorId == targetUserID {
			return echo.NewHTTPError(http.StatusConflict, "cluster creator cannot be removed as a member")
		}
		before, member, err := loadMember(c, db, clusterID, targetUserID)
		if err != nil {
			return err
		}
		if member == nil {
			return echo.NewHTTPError(http.StatusNotFound, "member not found")
		}
		if _, err := db.ClusterMember.Delete().
			Where(query.ClusterMember.ClusterId.Equals(clusterID), query.ClusterMember.UserId.Equals(targetUserID)).
			Do(c.Request().Context()); err != nil {
			return err
		}
		audit.SetResourceID(c, clusterID+":"+targetUserID)
		audit.SetChange(c, types.NewClusterMember(before), nil)
		return c.NoContent(http.StatusNoContent)
	}
}

// loadMember returns the membership row for (clusterID, userID) or
// (nil, nil) when the user is not a member. The first return value is the
// snapshot used for audit before-change capture.
func loadMember(c *echo.Context, db *client.Client, clusterID, userID string) (*model.ClusterMember, *model.ClusterMember, error) {
	current, err := findMember(c.Request().Context(), db, clusterID, userID)
	return current, current, err
}

func findMember(ctx context.Context, db *client.Client, clusterID, userID string) (*model.ClusterMember, error) {
	current, err := db.ClusterMember.Query().
		Where(query.ClusterMember.ClusterId.Equals(clusterID), query.ClusterMember.UserId.Equals(userID)).
		First(ctx)
	if err != nil {
		return nil, err
	}
	return current, nil
}
