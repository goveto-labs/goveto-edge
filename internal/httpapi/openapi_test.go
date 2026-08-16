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
	apikeysapi "goveto-edge/internal/httpapi/apikeys"
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
	"goveto-edge/internal/httpapi/users"
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
	initialization.Register(e, nil, nil, nil, nil, nil)
	authapi.Register(e, nil, nil, nil, nil, nil, nil, nil)
	adminsettings.Register(e, nil, nil, nil, nil, nil, nil)
	clusters.Register(e, nil, nil)
	apikeysapi.Register(e, nil, nil, nil)
	certificates.Register(e, nil, nil)
	dnsapi.Register(e, nil, nil, nil)
	nodes.Register(e, nil, nil, nil, nil, nil, nil)
	publishapi.Register(e, nil, nil)
	purgeapi.Register(e, nil, nil)
	jobsapi.Register(e, nil, nil)
	sites.Register(e, nil, nil, nil)
	auditapi.Register(e, nil)
	users.Register(e, nil, nil)
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

func TestOpenAPIDocumentsAPIKeyAuthenticationOnSupportedRoutes(t *testing.T) {
	data, err := os.ReadFile(openAPISpecPath)
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Components struct {
			SecuritySchemes map[string]yaml.Node `yaml:"securitySchemes"`
		} `yaml:"components"`
		Paths map[string]map[string]struct {
			Security []map[string][]string `yaml:"security"`
		} `yaml:"paths"`
	}
	if err = yaml.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	for _, scheme := range []string{"cookie_goveto_session", "bearer_api_key", "x_api_key"} {
		if _, found := spec.Components.SecuritySchemes[scheme]; !found {
			t.Errorf("security scheme %q is missing", scheme)
		}
	}
	assertSchemes := func(path, method string, want ...string) {
		t.Helper()
		operation, found := spec.Paths[path][method]
		if !found {
			t.Fatalf("operation %s %s is missing", strings.ToUpper(method), path)
		}
		got := make(map[string]bool)
		for _, requirement := range operation.Security {
			for scheme := range requirement {
				got[scheme] = true
			}
		}
		for _, scheme := range want {
			if !got[scheme] {
				t.Errorf("%s %s does not document %s authentication", strings.ToUpper(method), path, scheme)
			}
		}
	}
	assertSchemes("/api/v1/clusters/{cluster_id}/sites", "get",
		"cookie_goveto_session", "bearer_api_key", "x_api_key")
	assertSchemes("/api/v1/auth/me", "get", "cookie_goveto_session")
	if operation := spec.Paths["/api/v1/auth/me"]["get"]; len(operation.Security) != 1 {
		t.Errorf("user-only /auth/me advertises non-session credentials: %#v", operation.Security)
	}
}
