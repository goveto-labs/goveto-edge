// Package clusteraccess resolves cluster roles and enforces RBAC permissions.
package clusteraccess

import (
	"context"
	"net/http"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/auth"
	"goveto-edge/internal/rbac"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

// Require keeps the legacy read-access middleware behavior.
func Require(db *client.Client) echo.MiddlewareFunc {
	return RequirePermission(db, rbac.PermissionClusterRead)
}

// RequirePermission authorizes the current principal for a cluster capability.
func RequirePermission(db *client.Client, permission rbac.Permission) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			allowed, _, err := Authorize(c.Request().Context(), db, c.Param("cluster_id"), auth.CurrentUID(c), permission)
			if err != nil {
				return err
			}
			if !allowed {
				return echo.NewHTTPError(http.StatusForbidden, "permission denied: "+string(permission))
			}
			return next(c)
		}
	}
}

// Check preserves the legacy access tuple. owner means owner-level access and
// is true for both cluster owners and platform administrators.
func Check(ctx context.Context, db *client.Client, clusterID, uid string) (allowed, owner bool, err error) {
	allowed, role, err := Authorize(ctx, db, clusterID, uid, rbac.PermissionClusterRead)
	return allowed, role == rbac.RoleOwner || role == rbac.RoleAdmin, err
}

// Authorize resolves the effective cluster role and evaluates a permission.
func Authorize(ctx context.Context, db *client.Client, clusterID, uid string, permission rbac.Permission) (allowed bool, role rbac.Role, err error) {
	if clusterID == "" || uid == "" {
		return false, "", nil
	}
	user, cached := auth.CurrentUser(ctx, uid)
	if !cached {
		var err error
		user, err = db.User.FindUnique(ctx, query.User.Id.Equals(uid))
		if err != nil {
			return false, "", err
		}
	}
	if user == nil || user.Status != model.UserStatusACTIVE {
		return false, "", nil
	}
	cluster, err := db.Cluster.FindUnique(ctx, query.Cluster.Id.Equals(clusterID))
	if err != nil {
		return false, "", err
	}
	if cluster == nil {
		return false, "", nil
	}
	if user.Role == model.UserRoleADMIN {
		return rbac.SubjectForRole(rbac.RoleAdmin).Allows(permission), rbac.RoleAdmin, nil
	}
	if cluster.CreatorId == uid {
		return rbac.SubjectForRole(rbac.RoleOwner).Allows(permission), rbac.RoleOwner, nil
	}
	member, err := db.ClusterMember.Query().Where(query.ClusterMember.ClusterId.Equals(clusterID), query.ClusterMember.UserId.Equals(uid)).First(ctx)
	if err != nil {
		return false, "", err
	}
	if member == nil {
		return false, "", nil
	}
	switch member.Permission {
	case model.ClusterPermissionOWNER:
		role = rbac.RoleOwner
	case model.ClusterPermissionOPERATOR:
		role = rbac.RoleOperator
	case model.ClusterPermissionVIEWER:
		role = rbac.RoleViewer
	default:
		return false, "", nil
	}
	// A platform role is a minimum role in every cluster the user can access.
	role = rbac.Highest(role, rbac.Role(user.Role))
	return rbac.SubjectForRole(role).Allows(permission), role, nil
}

// RequirePlatform authorizes a platform-wide capability. Unlike
// RequirePermission it does not scope to a cluster; ADMIN users hold the
// platform.* permissions directly via the RBAC matrix, and everyone else is
// denied. This is the single source of truth for platform-management
// endpoints, replacing ad-hoc role == ADMIN checks.
func RequirePlatform(db *client.Client, permission rbac.Permission) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			allowed, err := AuthorizePlatform(c.Request().Context(), db, auth.CurrentUID(c), permission)
			if err != nil {
				return err
			}
			if !allowed {
				return echo.NewHTTPError(http.StatusForbidden, "permission denied: "+string(permission))
			}
			return next(c)
		}
	}
}

// AuthorizePlatform resolves a platform-wide permission without cluster scope.
// Only ADMIN users hold platform.* capabilities per the RBAC matrix.
func AuthorizePlatform(ctx context.Context, db *client.Client, uid string, permission rbac.Permission) (bool, error) {
	if uid == "" {
		return false, nil
	}
	user, cached := auth.CurrentUser(ctx, uid)
	if !cached {
		var err error
		user, err = db.User.FindUnique(ctx, query.User.Id.Equals(uid))
		if err != nil {
			return false, err
		}
	}
	if user == nil || user.Status != model.UserStatusACTIVE {
		return false, nil
	}
	return rbac.Allows(rbac.Role(user.Role), permission), nil
}
