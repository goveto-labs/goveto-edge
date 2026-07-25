// Package rbac defines authorization permissions and the role matrix shared by
// interactive users and future non-interactive principals.
package rbac

// Permission identifies one protected control-plane capability.
type Permission string

const (
	PermissionClusterRead       Permission = "cluster.read"
	PermissionSiteWrite         Permission = "site.write"
	PermissionSiteDelete        Permission = "site.delete"
	PermissionPublish           Permission = "site.publish"
	PermissionCacheOperate      Permission = "cache.operate"
	PermissionNodeManage        Permission = "node.manage"
	PermissionNodeDelete        Permission = "node.delete"
	PermissionCredentialManage  Permission = "credential.manage"
	PermissionCertificateManage Permission = "certificate.manage"
	PermissionCertificateDelete Permission = "certificate.delete"
	PermissionMemberManage      Permission = "cluster.member.manage"
	PermissionClusterTransfer   Permission = "cluster.transfer"

	PermissionPlatformUserManage     Permission = "platform.user.manage"
	PermissionPlatformSettingsManage Permission = "platform.settings.manage"
	PermissionPlatformAuditRead      Permission = "platform.audit.read"
	PermissionPlatformPolicyManage   Permission = "platform.policy.manage"
)

// Role is the effective role of a principal in the authorization scope.
// Platform roles are global minimums: a platform OPERATOR remains an operator
// in clusters where their membership is VIEWER, while ADMIN has global access.
type Role string

const (
	RoleViewer   Role = "VIEWER"
	RoleOperator Role = "OPERATOR"
	RoleOwner    Role = "OWNER"
	RoleAdmin    Role = "ADMIN"
)

// Subject is an authentication-source-neutral authorization principal. A nil
// permission scope uses the full role matrix; a non-nil scope restricts it.
type Subject struct {
	Role        Role
	permissions map[Permission]struct{}
}

// SubjectForRole creates an unrestricted interactive-user subject.
func SubjectForRole(role Role) Subject {
	return Subject{Role: role}
}

// ScopedSubject creates a role-capped subject for API tokens and service
// accounts. Passing no permissions creates a subject with no capabilities.
func ScopedSubject(role Role, permissions ...Permission) Subject {
	return Subject{Role: role, permissions: permissionSet(permissions...)}
}

// Allows reports whether the subject's role and optional scope both grant a
// permission.
func (subject Subject) Allows(permission Permission) bool {
	if !Allows(subject.Role, permission) {
		return false
	}
	if subject.permissions == nil {
		return true
	}
	_, ok := subject.permissions[permission]
	return ok
}

var rolePermissions = map[Role]map[Permission]struct{}{
	RoleViewer: permissionSet(
		PermissionClusterRead,
	),
	RoleOperator: permissionSet(
		PermissionClusterRead,
		PermissionSiteWrite,
		PermissionPublish,
		PermissionCacheOperate,
	),
	RoleOwner: permissionSet(
		PermissionClusterRead,
		PermissionSiteWrite,
		PermissionSiteDelete,
		PermissionPublish,
		PermissionCacheOperate,
		PermissionNodeManage,
		PermissionNodeDelete,
		PermissionCredentialManage,
		PermissionCertificateManage,
		PermissionCertificateDelete,
		PermissionMemberManage,
		PermissionClusterTransfer,
	),
	RoleAdmin: permissionSet(
		PermissionClusterRead,
		PermissionSiteWrite,
		PermissionSiteDelete,
		PermissionPublish,
		PermissionCacheOperate,
		PermissionNodeManage,
		PermissionNodeDelete,
		PermissionCredentialManage,
		PermissionCertificateManage,
		PermissionCertificateDelete,
		PermissionMemberManage,
		PermissionClusterTransfer,
		PermissionPlatformUserManage,
		PermissionPlatformSettingsManage,
		PermissionPlatformAuditRead,
		PermissionPlatformPolicyManage,
	),
}

// Allows reports whether role grants permission.
func Allows(role Role, permission Permission) bool {
	_, ok := rolePermissions[role][permission]
	return ok
}

// Highest returns the more privileged of two recognized roles. Cluster access
// uses it to combine a membership role with the user's global platform role.
func Highest(first, second Role) Role {
	if roleRank[second] > roleRank[first] {
		return second
	}
	return first
}

var roleRank = map[Role]int{
	RoleViewer:   1,
	RoleOperator: 2,
	RoleOwner:    3,
	RoleAdmin:    4,
}

func permissionSet(permissions ...Permission) map[Permission]struct{} {
	result := make(map[Permission]struct{}, len(permissions))
	for _, permission := range permissions {
		result[permission] = struct{}{}
	}
	return result
}
