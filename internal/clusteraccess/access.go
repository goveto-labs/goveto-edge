// Package clusteraccess resolves cluster roles and enforces RBAC permissions.
package clusteraccess

import (
	"context"
	"net/http"
	"time"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/auth"
	"goveto-edge/internal/rbac"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

// AuthorizedCluster is a cluster visible to the current request principal for
// a specific permission.
type AuthorizedCluster struct {
	ID        string    `db:"id" json:"id"`
	Name      string    `db:"name" json:"name"`
	Role      string    `db:"role" json:"role"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

// ListAuthorizedClusters returns only clusters for which the current request
// principal passes Authorize. API keys are restricted to their bound cluster.
func ListAuthorizedClusters(ctx context.Context, db *client.Client, uid string, permission rbac.Permission) ([]AuthorizedCluster, error) {
	if principal := auth.CurrentAPIKeyFromContext(ctx); principal != nil {
		allowed, _, err := AuthorizeAPIKey(principal, principal.ClusterID, permission)
		if err != nil || !allowed {
			return []AuthorizedCluster{}, err
		}
		cluster, err := db.Cluster.FindUnique(ctx, query.Cluster.Id.Equals(principal.ClusterID))
		if err != nil {
			return nil, err
		}
		if cluster == nil {
			return []AuthorizedCluster{}, nil
		}
		return []AuthorizedCluster{{
			ID: cluster.Id, Name: cluster.Name, Role: "API_KEY", CreatedAt: cluster.CreatedAt,
		}}, nil
	}
	if uid == "" {
		return []AuthorizedCluster{}, nil
	}
	user, cached := auth.CurrentUser(ctx, uid)
	if !cached {
		var err error
		user, err = db.User.FindUnique(ctx, query.User.Id.Equals(uid))
		if err != nil {
			return nil, err
		}
	}
	if user == nil || user.Status != model.UserStatusACTIVE {
		return []AuthorizedCluster{}, nil
	}
	type candidate struct {
		ID        string    `db:"id"`
		Name      string    `db:"name"`
		Role      string    `db:"role"`
		CreatedAt time.Time `db:"created_at"`
	}
	var candidates []candidate
	var err error
	if user.Role == model.UserRoleADMIN {
		candidates, err = client.Raw[candidate](ctx, db,
			`SELECT id, name, 'ADMIN' AS role, created_at FROM clusters ORDER BY created_at, name`)
	} else {
		candidates, err = client.Raw[candidate](ctx, db, `SELECT id, name, role, created_at FROM (
			SELECT DISTINCT ON (c.id) c.id, c.name, c.created_at,
				CASE WHEN c.creator_id=$1 THEN 'OWNER' ELSE cm.permission::text END AS role
			FROM clusters c LEFT JOIN cluster_members cm ON cm.cluster_id=c.id AND cm.user_id=$1
			WHERE c.creator_id=$1 OR cm.user_id=$1 ORDER BY c.id
		) visible ORDER BY created_at, name`, uid)
	}
	if err != nil {
		return nil, err
	}
	result := make([]AuthorizedCluster, 0, len(candidates))
	for _, item := range candidates {
		role := rbac.RoleAdmin
		if user.Role != model.UserRoleADMIN {
			var valid bool
			role, valid = membershipRole(model.ClusterPermission(item.Role))
			if !valid {
				continue
			}
			role = rbac.Highest(role, rbac.Role(user.Role))
		}
		if rbac.SubjectForRole(role).Allows(permission) {
			result = append(result, AuthorizedCluster{
				ID: item.ID, Name: item.Name, Role: string(role), CreatedAt: item.CreatedAt,
			})
		}
	}
	return result, nil
}

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
// Requests authenticated with an API key are routed to the key path first:
// the key must be bound to the requested cluster and the permission must be
// in its grant scope.
func Authorize(ctx context.Context, db *client.Client, clusterID, uid string, permission rbac.Permission) (allowed bool, role rbac.Role, err error) {
	if principal := auth.CurrentAPIKeyFromContext(ctx); principal != nil {
		return AuthorizeAPIKey(principal, clusterID, permission)
	}
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
	role, valid := membershipRole(member.Permission)
	if !valid {
		return false, "", nil
	}
	// A platform role is a minimum role in every cluster the user can access.
	role = rbac.Highest(role, rbac.Role(user.Role))
	return rbac.SubjectForRole(role).Allows(permission), role, nil
}

func membershipRole(permission model.ClusterPermission) (rbac.Role, bool) {
	switch permission {
	case model.ClusterPermissionOWNER:
		return rbac.RoleOwner, true
	case model.ClusterPermissionOPERATOR:
		return rbac.RoleOperator, true
	case model.ClusterPermissionVIEWER:
		return rbac.RoleViewer, true
	default:
		return "", false
	}
}

// AuthorizeAPIKey evaluates a permission for an API key principal. The key
// is hard-bound to its cluster: a mismatched cluster_id in the route is
// always denied regardless of the requested permission.
func AuthorizeAPIKey(principal *auth.APIKeyPrincipal, clusterID string, permission rbac.Permission) (allowed bool, role rbac.Role, err error) {
	if principal == nil || clusterID == "" || clusterID != principal.ClusterID {
		return false, "", nil
	}
	// RoleOwner is only the carrier used to evaluate the grantable permission
	// matrix. API keys do not hold an ownership role, so never expose it.
	subject := rbac.ScopedSubject(rbac.RoleOwner, principal.Permissions...)
	return subject.Allows(permission), "", nil
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
// Only ADMIN users hold platform.* capabilities per the RBAC matrix; API key
// principals never hold platform capabilities.
func AuthorizePlatform(ctx context.Context, db *client.Client, uid string, permission rbac.Permission) (bool, error) {
	if auth.CurrentAPIKeyFromContext(ctx) != nil {
		return false, nil
	}
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
