package clusteraccess

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/auth"
	"goveto-edge/internal/rbac"
)

func keyPrincipal(clusterID string, permissions ...rbac.Permission) *auth.APIKeyPrincipal {
	return &auth.APIKeyPrincipal{
		KeyID: "key", ClusterID: clusterID, Prefix: "gve1_Ab12cDe", Permissions: permissions,
	}
}

func TestAuthorizeAPIKeyBoundClusterOnly(t *testing.T) {
	principal := keyPrincipal("cluster-a", rbac.PermissionClusterRead, rbac.PermissionPublish)

	allowed, _, err := AuthorizeAPIKey(principal, "cluster-a", rbac.PermissionClusterRead)
	if err != nil || !allowed {
		t.Fatalf("granted permission on the bound cluster: allowed=%v err=%v", allowed, err)
	}
	allowed, _, err = AuthorizeAPIKey(principal, "cluster-b", rbac.PermissionClusterRead)
	if err != nil || allowed {
		t.Fatalf("key must never access another cluster: allowed=%v err=%v", allowed, err)
	}
	allowed, _, err = AuthorizeAPIKey(principal, "", rbac.PermissionClusterRead)
	if err != nil || allowed {
		t.Fatalf("empty cluster id must be denied: allowed=%v err=%v", allowed, err)
	}
	allowed, _, err = AuthorizeAPIKey(nil, "cluster-a", rbac.PermissionClusterRead)
	if err != nil || allowed {
		t.Fatalf("nil principal must be denied: allowed=%v err=%v", allowed, err)
	}
}

func TestAuthorizeAPIKeyScopeRestrictsPermissions(t *testing.T) {
	principal := keyPrincipal("cluster-a", rbac.PermissionClusterRead)

	if allowed, _, _ := AuthorizeAPIKey(principal, "cluster-a", rbac.PermissionPublish); allowed {
		t.Fatal("permission outside the key scope must be denied")
	}
	if allowed, _, _ := AuthorizeAPIKey(principal, "cluster-a", rbac.PermissionAPIKeyManage); allowed {
		t.Fatal("key management must be denied even though the carrier role is OWNER")
	}
}

// TestAuthorizeRoutesAPIKeyFirst verifies that the request-context principal
// short-circuits before the uid guard, which is what lets the
// RequirePermission middleware authorize uid-less api key requests. A nil db
// proves the key branch never touches the database.
func TestAuthorizeRoutesAPIKeyFirst(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/v1/clusters/cluster-a/sites", nil)
	response := httptest.NewRecorder()
	c := &echo.Context{}
	c.SetRequest(request)
	c.SetResponse(response)
	auth.SetCurrentAPIKey(c, keyPrincipal("cluster-a", rbac.PermissionClusterRead))

	allowed, role, err := Authorize(c.Request().Context(), nil, "cluster-a", "", rbac.PermissionClusterRead)
	if err != nil || !allowed {
		t.Fatalf("api key request through Authorize: allowed=%v err=%v", allowed, err)
	}
	if role != "" {
		t.Fatalf("api key leaked carrier ownership role %q", role)
	}
	if allowed, owner, checkErr := Check(c.Request().Context(), nil, "cluster-a", ""); checkErr != nil || !allowed || owner {
		t.Fatalf("Check reported api key as owner: allowed=%v owner=%v err=%v", allowed, owner, checkErr)
	}
	allowed, _, err = Authorize(c.Request().Context(), nil, "cluster-a", "", rbac.PermissionSiteDelete)
	if err != nil || allowed {
		t.Fatalf("permission outside scope must be denied: allowed=%v err=%v", allowed, err)
	}
}

// TestAuthorizePlatformDeniesAPIKeys covers the platform guard.
func TestAuthorizePlatformDeniesAPIKeys(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	response := httptest.NewRecorder()
	c := &echo.Context{}
	c.SetRequest(request)
	c.SetResponse(response)
	auth.SetCurrentAPIKey(c, keyPrincipal("cluster-a", rbac.PermissionClusterRead))

	allowed, err := AuthorizePlatform(c.Request().Context(), nil, "", rbac.PermissionPlatformUserManage)
	if err != nil || allowed {
		t.Fatalf("api keys must never hold platform capabilities: allowed=%v err=%v", allowed, err)
	}
}
