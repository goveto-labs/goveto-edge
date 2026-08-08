package httpapi_test

// This test guards the audit mutation manifest in internal/audit/routes.go
// against drift: every registered HTTP mutation (POST/PUT/PATCH/DELETE) must
// be declared in the manifest so it is audited, and every declared mutation
// must correspond to a registered route. GET routes are read-only and are
// audited only when explicitly declared (e.g. bootstrap identity download).

import (
	"net/http"
	"sort"
	"testing"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/analytics"
	"goveto-edge/internal/audit"
	"goveto-edge/internal/httpapi/adminsettings"
	analyticsapi "goveto-edge/internal/httpapi/analytics"
	"goveto-edge/internal/httpapi/audit"
	authapi "goveto-edge/internal/httpapi/auth"
	"goveto-edge/internal/httpapi/certificates"
	"goveto-edge/internal/httpapi/clusters"
	dnsapi "goveto-edge/internal/httpapi/dns"
	"goveto-edge/internal/httpapi/health"
	"goveto-edge/internal/httpapi/initialization"
	jobsapi "goveto-edge/internal/httpapi/jobs"
	"goveto-edge/internal/httpapi/nodes"
	publishapi "goveto-edge/internal/httpapi/publish"
	purgeapi "goveto-edge/internal/httpapi/purge"
	"goveto-edge/internal/httpapi/sites"
)

var mutationMethods = map[string]bool{
	http.MethodPost:   true,
	http.MethodPut:    true,
	http.MethodPatch:  true,
	http.MethodDelete: true,
}

func collectRegisteredMutations(t *testing.T) map[string]bool {
	t.Helper()
	e := echo.New()
	routes := map[string]bool{}
	e.OnAddRoute = func(route echo.Route) error {
		if route.Method == echo.RouteNotFound {
			return nil
		}
		if mutationMethods[route.Method] {
			routes[route.Method+" "+route.Path] = true
		}
		return nil
	}
	health.Register(e, nil)
	initialization.Register(e, nil, nil, nil)
	authapi.Register(e, nil, nil, nil, nil, nil, nil)
	adminsettings.Register(e, nil, nil, nil)
	clusters.Register(e, nil, nil)
	certificates.Register(e, nil, nil)
	dnsapi.Register(e, nil, nil, nil)
	nodes.Register(e, nil, nil, nil, nil, nil, nil)
	publishapi.Register(e, nil, nil)
	purgeapi.Register(e, nil, nil)
	jobsapi.Register(e, nil, nil)
	sites.Register(e, nil, nil, nil, nil)
	auditapi.Register(e, nil)
	analyticsapi.Register(e, nil, analytics.NewStore(nil, 0))
	return routes
}

func TestAuditManifestCoversEveryRegisteredMutation(t *testing.T) {
	registered := collectRegisteredMutations(t)
	declared := map[string]bool{}
	declaredMutations := map[string]bool{}
	for _, route := range audit.ControlPlaneRoutes {
		key := route.Method + " " + route.Path
		declared[key] = true
		if mutationMethods[route.Method] {
			declaredMutations[key] = true
		}
	}

	var undeclared, stale []string
	for route := range registered {
		if !declared[route] {
			undeclared = append(undeclared, route)
		}
	}
	for route := range declaredMutations {
		if !registered[route] {
			stale = append(stale, route)
		}
	}
	sort.Strings(undeclared)
	sort.Strings(stale)
	for _, route := range undeclared {
		t.Errorf("registered mutation is not in audit manifest: %s\nadd it to internal/audit/routes.go or mark it read-only", route)
	}
	for _, route := range stale {
		t.Errorf("audit manifest declares a mutation that is no longer registered: %s\nremove it from internal/audit/routes.go", route)
	}
}
