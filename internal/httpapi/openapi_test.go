package httpapi_test

// This test guards docs/openapi.yaml against drift: every route registered on
// the control-plane echo instance must be documented in the specification and
// every documented operation must exist in code.

import (
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"gopkg.in/yaml.v3"

	"goveto-edge/internal/analytics"
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

// openAPISpecPath is relative to this package's directory.
const openAPISpecPath = "../../docs/openapi.yaml"

var openAPIParamPattern = regexp.MustCompile(`\{[^}/]+\}`)

// routeKey identifies one documented operation: "GET /api/v1/clusters/:cluster_id".
type routeKey string

func collectRegisteredRoutes(t *testing.T) map[routeKey]bool {
	t.Helper()
	e := echo.New()
	routes := map[routeKey]bool{}
	e.OnAddRoute = func(route echo.Route) error {
		// Echo v5.3 registers an internal 404 route per middleware group so
		// group middleware also runs for unmatched paths. Those are not API
		// operations and do not belong in the specification.
		if route.Method == echo.RouteNotFound {
			return nil
		}
		routes[routeKey(route.Method+" "+route.Path)] = true
		return nil
	}
	// Registration only wires handlers as closures, so nil dependencies are
	// safe here: nothing dereferences them until a request is served.
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

func documentedOperations(t *testing.T) map[routeKey]bool {
	t.Helper()
	data, err := os.ReadFile(openAPISpecPath)
	if err != nil {
		t.Fatalf("read OpenAPI specification: %v", err)
	}
	var spec struct {
		OpenAPI string                          `yaml:"openapi"`
		Paths   map[string]map[string]yaml.Node `yaml:"paths"`
	}
	if err := yaml.Unmarshal(data, &spec); err != nil {
		t.Fatalf("parse OpenAPI specification: %v", err)
	}
	if !strings.HasPrefix(spec.OpenAPI, "3.") {
		t.Fatalf("unsupported OpenAPI version %q", spec.OpenAPI)
	}
	routes := map[routeKey]bool{}
	for path, operations := range spec.Paths {
		echoPath := openAPIParamPattern.ReplaceAllStringFunc(path, func(param string) string {
			return ":" + param[1:len(param)-1]
		})
		for method := range operations {
			switch strings.ToUpper(method) {
			case http.MethodGet, http.MethodPut, http.MethodPost,
				http.MethodDelete, http.MethodPatch, http.MethodHead,
				http.MethodOptions, http.MethodTrace:
				routes[routeKey(strings.ToUpper(method)+" "+echoPath)] = true
			}
		}
	}
	return routes
}

func TestOpenAPISpecMatchesRegisteredRoutes(t *testing.T) {
	registered := collectRegisteredRoutes(t)
	documented := documentedOperations(t)

	var undocumented, stale []string
	for route := range registered {
		if !documented[route] {
			undocumented = append(undocumented, string(route))
		}
	}
	for route := range documented {
		if !registered[route] {
			stale = append(stale, string(route))
		}
	}
	sort.Strings(undocumented)
	sort.Strings(stale)
	for _, route := range undocumented {
		t.Errorf("route missing from docs/openapi.yaml: %s", route)
	}
	for _, route := range stale {
		t.Errorf("docs/openapi.yaml documents unregistered route: %s", route)
	}
}
