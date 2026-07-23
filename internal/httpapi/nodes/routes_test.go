package nodes

import (
	"net/http"
	"testing"

	"github.com/labstack/echo/v5"
)

func TestRegisterIncludesSSHCredentialRoutes(t *testing.T) {
	e := echo.New()
	Register(e, nil, nil, nil, nil)

	routes := e.Router().Routes()
	for _, expected := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/clusters/:cluster_id/ssh-credentials"},
		{http.MethodPost, "/api/v1/clusters/:cluster_id/ssh-credentials"},
		{http.MethodPut, "/api/v1/clusters/:cluster_id/ssh-credentials/:credential_id"},
		{http.MethodDelete, "/api/v1/clusters/:cluster_id/ssh-credentials/:credential_id"},
		{http.MethodGet, "/api/v1/clusters/:cluster_id/ssh-credentials/:credential_id/nodes"},
	} {
		if _, err := routes.FindByMethodPath(expected.method, expected.path); err != nil {
			t.Errorf("route %s %s is not registered", expected.method, expected.path)
		}
	}
}
