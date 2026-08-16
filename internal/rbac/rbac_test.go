package rbac

import "testing"

func TestRolePermissionMatrix(t *testing.T) {
	clusterPermissions := []Permission{
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
		PermissionNotificationManage,
		PermissionLogpushManage,
	}
	tests := []struct {
		role    Role
		allowed map[Permission]bool
	}{
		{RoleViewer, map[Permission]bool{
			PermissionClusterRead: true,
		}},
		{RoleOperator, map[Permission]bool{
			PermissionClusterRead:  true,
			PermissionSiteWrite:    true,
			PermissionPublish:      true,
			PermissionCacheOperate: true,
		}},
		{RoleOwner, all(clusterPermissions...)},
		{RoleAdmin, all(clusterPermissions...)},
	}

	for _, test := range tests {
		t.Run(string(test.role), func(t *testing.T) {
			for _, permission := range clusterPermissions {
				if got := Allows(test.role, permission); got != test.allowed[permission] {
					t.Errorf("Allows(%s, %s) = %t, want %t", test.role, permission, got, test.allowed[permission])
				}
			}
		})
	}

	for _, permission := range []Permission{
		PermissionPlatformUserManage,
		PermissionPlatformClusterRead,
		PermissionPlatformSettingsManage,
		PermissionPlatformAuditRead,
		PermissionPlatformPolicyManage,
	} {
		if !Allows(RoleAdmin, permission) {
			t.Errorf("admin must have %s", permission)
		}
		for _, role := range []Role{RoleViewer, RoleOperator, RoleOwner} {
			if Allows(role, permission) {
				t.Errorf("%s must not have platform permission %s", role, permission)
			}
		}
	}
}

func TestScopedSubjectCannotExceedRoleOrScope(t *testing.T) {
	subject := ScopedSubject(RoleOperator, PermissionClusterRead, PermissionCredentialManage)
	if !subject.Allows(PermissionClusterRead) {
		t.Fatal("scoped operator should retain an allowed scoped permission")
	}
	if subject.Allows(PermissionSiteWrite) {
		t.Fatal("scope must restrict permissions otherwise granted by the role")
	}
	if subject.Allows(PermissionCredentialManage) {
		t.Fatal("scope must not elevate a role")
	}
	if ScopedSubject(RoleAdmin).Allows(PermissionClusterRead) {
		t.Fatal("an empty explicit scope must grant no permissions")
	}
}

func TestHighestRole(t *testing.T) {
	if got := Highest(RoleViewer, RoleOperator); got != RoleOperator {
		t.Fatalf("highest role = %s, want %s", got, RoleOperator)
	}
	if got := Highest(RoleOwner, RoleViewer); got != RoleOwner {
		t.Fatalf("highest role = %s, want %s", got, RoleOwner)
	}
}

func all(permissions ...Permission) map[Permission]bool {
	result := make(map[Permission]bool, len(permissions))
	for _, permission := range permissions {
		result[permission] = true
	}
	return result
}

func TestAPIKeyManagePermissionRoles(t *testing.T) {
	for _, role := range []Role{RoleOwner, RoleAdmin} {
		if !Allows(role, PermissionAPIKeyManage) {
			t.Errorf("%s must manage api keys", role)
		}
	}
	for _, role := range []Role{RoleViewer, RoleOperator} {
		if Allows(role, PermissionAPIKeyManage) {
			t.Errorf("%s must not manage api keys", role)
		}
	}
}

func TestKeyGrantablePermissionsWhitelist(t *testing.T) {
	grantable := KeyGrantablePermissions()
	want := []Permission{
		PermissionClusterRead,
		PermissionSiteWrite,
		PermissionSiteDelete,
		PermissionPublish,
		PermissionCacheOperate,
	}
	if len(grantable) != len(want) {
		t.Fatalf("grantable set %v drifted from the expected whitelist %v", grantable, want)
	}
	for index := range want {
		if grantable[index] != want[index] {
			t.Fatalf("grantable set %v drifted from the expected whitelist %v", grantable, want)
		}
	}
	// The whitelist must never leak capabilities that would let an api key
	// persist itself or reach secrets and identity management.
	forbidden := []Permission{
		PermissionAPIKeyManage, PermissionCredentialManage, PermissionNodeManage,
		PermissionNodeDelete, PermissionCertificateManage, PermissionMemberManage,
		PermissionClusterTransfer, PermissionNotificationManage,
		PermissionLogpushManage,
		PermissionPlatformUserManage, PermissionPlatformClusterRead,
		PermissionPlatformSettingsManage, PermissionPlatformAuditRead,
		PermissionPlatformPolicyManage,
	}
	for _, permission := range forbidden {
		if KeyPermissionAllowed(permission) {
			t.Errorf("permission %q must not be grantable to an api key", permission)
		}
	}
	// Every grantable permission must be a subset of the OWNER matrix so a
	// scoped OWNER subject is the correct carrier for key authorization.
	for _, permission := range grantable {
		if !Allows(RoleOwner, permission) {
			t.Errorf("grantable permission %q is missing from the OWNER matrix", permission)
		}
	}
}
