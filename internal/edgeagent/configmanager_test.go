package edgeagent

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/caddyserver/caddy/v2"

	"goveto-edge/internal/edgeprotocol"
	cachepolicy "goveto-edge/internal/policy"

	"goveto-edge/caddy/agentlog"
)

type nopLogSink struct{}

func (nopLogSink) WriteCaddyLog(string, uint64, []byte) error { return nil }

func ensureAgentLogSink(t *testing.T) {
	t.Helper()
	agentlog.SetSink(nopLogSink{})
	t.Cleanup(func() { agentlog.SetSink(nil) })
}

func TestApplySitePersistenceFailureDoesNotAdvanceVersion(t *testing.T) {
	ensureAgentLogSink(t)
	parent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parent, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	agentPort := freePort(t)
	manager := NewConfigManager(filepath.Join(parent, "sites.json"), ":"+strconv.Itoa(agentPort))
	config := validHTTPConfig(t)
	first := manager.ApplySite(config)
	second := manager.ApplySite(config)
	if first == nil || second == nil {
		t.Fatal("expected persistence failures")
	}
	if !strings.Contains(first.Error(), "persist pending site config") {
		t.Fatalf("expected persist failure, got %v", first)
	}
	if strings.Contains(second.Error(), "version is not newer") {
		t.Fatalf("version was advanced after persistence failure: %v", second)
	}
	if manager.ConfigVersion() != 0 {
		t.Fatalf("config version advanced after failure: %d", manager.ConfigVersion())
	}
	_ = manager.Stop()
}

func TestApplySitePersistsAndRejectsStaleVersion(t *testing.T) {
	ensureAgentLogSink(t)
	path := filepath.Join(t.TempDir(), "sites.json")
	agentPort := freePort(t)
	manager := NewConfigManager(path, ":"+strconv.Itoa(agentPort))
	config := validHTTPConfig(t)
	if err := manager.ApplySite(config); err != nil {
		t.Fatalf("apply site: %v", err)
	}
	if manager.ConfigVersion() != 1 {
		t.Fatalf("config version: %d", manager.ConfigVersion())
	}
	if versions := manager.SiteVersions(); versions["site-1"] != 1 {
		t.Fatalf("site versions: %#v", versions)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]SiteConfig
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	if stored["site-1"].Version != 1 {
		t.Fatalf("persisted version: %#v", stored)
	}
	if err := manager.ApplySite(config); err == nil || !strings.Contains(err.Error(), "version is not newer") {
		t.Fatalf("expected stale version error, got %v", err)
	}
	config.Version = 2
	config.Domains = []string{"updated.example.com"}
	if err := manager.ApplySite(config); err != nil {
		t.Fatalf("apply newer version: %v", err)
	}
	if manager.ConfigVersion() != 2 {
		t.Fatalf("config version after upgrade: %d", manager.ConfigVersion())
	}
	if err := manager.Stop(); err != nil {
		t.Fatal(err)
	}
	restored := NewConfigManager(path, ":"+strconv.Itoa(freePort(t)))
	if err := restored.Restore(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restored.ConfigVersion() != 2 {
		t.Fatalf("restored config version: %d", restored.ConfigVersion())
	}
	_ = restored.Stop()
}

func TestApplyHTTPConfigProxiesMatchedHost(t *testing.T) {
	ensureAgentLogSink(t)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("proxied:" + r.URL.Path))
	}))
	defer origin.Close()

	originAddress := strings.TrimPrefix(origin.URL, "http://")
	port := freePort(t)
	manager := NewConfigManager(filepath.Join(t.TempDir(), "sites.json"), ":"+strconv.Itoa(port))
	config := SiteConfig{
		SiteID:   "site-http",
		Version:  1,
		Domains:  []string{"site.example.test"},
		Listener: ListenerConfig{HTTPEnabled: true, HTTPPort: port},
		Origins:  []OriginConfig{{Protocol: "http", Address: originAddress}},
	}
	if err := manager.ApplySite(config); err != nil {
		t.Fatalf("apply site: %v", err)
	}
	defer manager.Stop()

	request, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+"/hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "site.example.test"
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("request site: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || string(body) != "proxied:/hello" {
		t.Fatalf("unexpected proxy response: status=%d body=%q", response.StatusCode, body)
	}

	unmatched, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	unmatched.Host = "unknown.example.test"
	unmatchedResponse, err := http.DefaultClient.Do(unmatched)
	if err != nil {
		t.Fatalf("request unmatched host: %v", err)
	}
	defer unmatchedResponse.Body.Close()
	if unmatchedResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("unmatched host status=%d, want 404", unmatchedResponse.StatusCode)
	}
}

func TestRenderCaddyConfigIncludesACMEHTTPChallengeBeforeSiteRoutes(t *testing.T) {
	config := SiteConfig{
		SiteID: "site-acme", Version: 1, Domains: []string{"acme.example.com"},
		Listener: ListenerConfig{HTTPEnabled: true, HTTPPort: 8080},
		Origins:  []OriginConfig{{Protocol: "http", Address: "origin:80"}},
	}
	config.ACMEChallenges = []ACMEChallengeConfig{{Domain: config.Domains[0], Token: "token-1", KeyAuth: "token-1.thumbprint"}}
	encoded, err := renderCaddyConfig(map[string]SiteConfig{config.SiteID: config}, ":8080", "")
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	challengePath := "/.well-known/acme-challenge/token-1"
	if !strings.Contains(text, challengePath) || !strings.Contains(text, "token-1.thumbprint") {
		t.Fatalf("challenge route missing from %s", text)
	}
}

func TestRenderCaddyConfigBindsSiteMetadataToAccessLogger(t *testing.T) {
	config := validHTTPConfig(t)
	config.Version = 42
	encoded, err := renderCaddyConfig(map[string]SiteConfig{config.SiteID: config}, ":80", "")
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, expected := range []string{
		`"logger_names":{"` + config.Domains[0] + `":["site_` + config.SiteID + `"]}`,
		`"config_version":42`, `"site_id":"` + config.SiteID + `"`,
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("site access logger metadata %s missing from %s", expected, text)
		}
	}
}

func TestHTTPSOriginRendersTLSTransport(t *testing.T) {
	config := validHTTPConfig(t)
	config.Origins[0].Protocol = "https"
	encoded, err := renderCaddyConfig(map[string]SiteConfig{config.SiteID: config}, ":80", "550e8400-e29b-41d4-a716-446655440000")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"protocol":"goveto_http"`) || !strings.Contains(string(encoded), `"tls":{`) {
		t.Fatalf("HTTPS transport missing: %s", encoded)
	}
}

func TestRenderCaddyConfigHTTPSite(t *testing.T) {
	config := validHTTPConfig(t)
	config.Scheduler = "ip_hash"
	config.Origins[0].HostHeader = "origin.internal"
	config.Cache = enabledCachePolicy(t)
	encoded, err := renderCaddyConfig(map[string]SiteConfig{config.SiteID: config}, ":80", "550e8400-e29b-41d4-a716-446655440000")
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(encoded, &parsed); err != nil {
		t.Fatal(err)
	}
	if admin, _ := parsed["admin"].(map[string]any); admin["disabled"] != true {
		t.Fatalf("admin should be disabled: %#v", parsed["admin"])
	}
	apps := parsed["apps"].(map[string]any)
	httpApp := apps["http"].(map[string]any)
	servers := httpApp["servers"].(map[string]any)
	edge := servers["edge"].(map[string]any)
	listen := asStringSlice(edge["listen"])
	sitePort := ":" + strconv.Itoa(config.Listener.HTTPPort)
	if !contains(listen, ":80") || !contains(listen, sitePort) {
		t.Fatalf("unexpected listeners: %#v", listen)
	}
	routes := edge["routes"].([]any)
	if len(routes) < 1 {
		t.Fatalf("expected site routes, got %d", len(routes))
	}
	raw := string(encoded)
	if strings.Contains(raw, "goveto_agent") {
		t.Fatalf("management API leaked onto the user traffic listener: %s", raw)
	}
	if !strings.Contains(raw, `"policy":"goveto_origin"`) || !strings.Contains(raw, `"scheduler":"ip_hash"`) {
		t.Fatalf("ip_hash was not mapped: %s", raw)
	}
	if !strings.Contains(raw, `"host_header":"origin.internal"`) || !strings.Contains(raw, `"Host":["{goveto.origin.host}"]`) {
		t.Fatalf("host header missing: %s", raw)
	}
	if !strings.Contains(raw, `"handler":"cache"`) {
		t.Fatalf("cache handler missing: %s", raw)
	}
	if !strings.Contains(raw, `"output":"goveto_buffer"`) {
		t.Fatalf("log buffer writer missing: %s", raw)
	}
}

func TestRenderCaddyConfigHTTPSSite(t *testing.T) {
	config := validHTTPSConfig(t)
	encoded, err := renderCaddyConfig(map[string]SiteConfig{config.SiteID: config}, ":80", "node-host")
	if err != nil {
		t.Fatal(err)
	}
	raw := string(encoded)
	httpsPort := ":" + strconv.Itoa(config.Listener.HTTPSPort)
	for _, want := range []string{
		`"status_code":301`,
		`"Strict-Transport-Security":["max-age=31536000; includeSubDomains; preload"]`,
		`"protocol_min":"tls1.3"`,
		`"certificate":"CERT"`,
		`"key":"KEY"`,
		`"h2"`,
		`"h3"`,
		httpsPort,
	} {
		if !strings.Contains(raw, want) {
			t.Fatalf("missing %s in config: %s", want, raw)
		}
	}
}

func TestRenderCaddyConfigMultipleSitesSorted(t *testing.T) {
	a := validHTTPConfig(t)
	a.SiteID = "site-b"
	a.Domains = []string{"b.example.com"}
	b := validHTTPConfig(t)
	b.SiteID = "site-a"
	b.Domains = []string{"a.example.com"}
	encoded, err := renderCaddyConfig(map[string]SiteConfig{
		a.SiteID: a,
		b.SiteID: b,
	}, ":80", "node-host")
	if err != nil {
		t.Fatal(err)
	}
	raw := string(encoded)
	idxA := strings.Index(raw, `"@id":"site_site-a"`)
	idxB := strings.Index(raw, `"@id":"site_site-b"`)
	if idxA < 0 || idxB < 0 || idxA > idxB {
		t.Fatalf("sites not sorted by id: a=%d b=%d raw=%s", idxA, idxB, raw)
	}
}

func TestRenderCaddyConfigRejectsUnknownScheduler(t *testing.T) {
	config := validHTTPConfig(t)
	config.Scheduler = "not_a_real_policy"
	_, err := renderCaddyConfig(map[string]SiteConfig{config.SiteID: config}, ":80", "node-host")
	if err == nil {
		t.Fatal("expected unknown scheduler to fail")
	}
}

func TestMixedOriginProtocolsRejected(t *testing.T) {
	config := validHTTPConfig(t)
	config.Origins = append(config.Origins, OriginConfig{Protocol: "https", Address: "origin-2:443"})
	if err := config.Validate(); err == nil {
		t.Fatal("expected mixed protocols to be rejected")
	}
}

func TestRenderCaddyConfigMapsOriginGovernance(t *testing.T) {
	config := validHTTPConfig(t)
	config.Origins = []OriginConfig{
		{Protocol: "https", Address: "[2001:db8::1]:443", HostHeader: "primary.internal", Weight: 7},
		{Protocol: "https", Address: "[2001:db8::2]:443", HostHeader: "backup.internal", Weight: 2, Priority: 10},
	}
	config.OriginPolicy = edgeprotocol.OriginPolicyConfig{
		HealthURI: "/ready?deep=1", TimeoutMS: 17000,
		Headers: map[string][]string{"X-Origin-Token": {"secret"}},
		ActiveHealth: edgeprotocol.OriginActiveHealthConfig{
			Enabled: true, Method: "HEAD", Host: "health.internal",
			Headers: map[string][]string{"X-Health": {"probe"}}, ExpectedStatus: 204,
			ExpectedBody: "ready", IntervalMS: 4000, TimeoutMS: 1200, Passes: 3, Fails: 4,
		},
		PassiveHealth: edgeprotocol.OriginPassiveHealthConfig{
			Enabled: true, FailDurationMS: 45000, MaxFails: 5,
			UnhealthyStatus: []int{500, 503}, UnhealthyLatencyMS: 2500, UnhealthyRequestCount: 64,
		},
		Transport: edgeprotocol.OriginTransportConfig{
			DialTimeoutMS: 1100, TLSHandshakeTimeoutMS: 2200, ResponseHeaderTimeoutMS: 3300,
			ReadTimeoutMS: 4400, WriteTimeoutMS: 5500, IPVersion: "ipv6",
			TLSServerName: "tls.internal", TLSInsecureSkipVerify: true,
			TLSClientCertificatePEM: "CLIENT CERT", TLSClientPrivateKeyPEM: "CLIENT KEY",
		},
		Retry: edgeprotocol.OriginRetryConfig{Retries: 4, TryDurationMS: 6000, TryIntervalMS: 300},
	}
	encoded, err := renderCaddyConfig(map[string]SiteConfig{config.SiteID: config}, ":80", "node-host")
	if err != nil {
		t.Fatal(err)
	}
	raw := string(encoded)
	for _, expected := range []string{
		`"dial":"tcp6/[2001:db8::1]:443"`, `"weight":7`, `"priority":10`,
		`"uri":"/ready?deep=1"`, `"method":"HEAD"`, `"Host":["health.internal"]`,
		`"expect_status":204`, `"expect_body":"ready"`, `"passes":3`, `"fails":4`,
		`"max_fails":5`, `"unhealthy_status":[500,503]`, `"unhealthy_request_count":64`,
		`"dial_timeout":1100000000`, `"handshake_timeout":2200000000`,
		`"response_header_timeout":3300000000`, `"read_timeout":4400000000`, `"write_timeout":5500000000`,
		`"server_name":"tls.internal"`, `"insecure_skip_verify":true`,
		`"client_certificate_pem":"CLIENT CERT"`, `"client_private_key_pem":"CLIENT KEY"`,
		`"X-Origin-Token":["secret"]`, `"handler":"goveto_origin_metrics"`, `"timeout":17000000000`,
		`"retries":4`, `"try_duration":6000000000`, `"try_interval":300000000`,
	} {
		if !strings.Contains(raw, expected) {
			t.Fatalf("missing origin governance setting %s in %s", expected, raw)
		}
	}
	if !strings.Contains(raw, `"method":["GET","HEAD","PUT","DELETE","OPTIONS","TRACE"]`) || strings.Contains(raw, `"method":["GET","HEAD","POST"`) {
		t.Fatalf("retry matcher is not restricted to idempotent methods: %s", raw)
	}
}

func TestApplySiteUsesPerOriginHostHeaders(t *testing.T) {
	ensureAgentLogSink(t)
	origins := make([]*httptest.Server, 0, 2)
	addresses := make([]string, 0, 2)
	for index := range 2 {
		name := "origin-" + strconv.Itoa(index+1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			_, _ = w.Write([]byte(name + ":" + request.Host))
		}))
		origins = append(origins, server)
		addresses = append(addresses, strings.TrimPrefix(server.URL, "http://"))
	}
	defer func() {
		for _, server := range origins {
			server.Close()
		}
	}()

	port := freePort(t)
	manager := NewConfigManager(filepath.Join(t.TempDir(), "sites.json"), ":"+strconv.Itoa(port))
	config := SiteConfig{
		SiteID: "host-isolation", Version: 1, Domains: []string{"hosts.example.test"},
		Listener: ListenerConfig{HTTPEnabled: true, HTTPPort: port},
		Origins: []OriginConfig{
			{Protocol: "http", Address: addresses[0], HostHeader: "one.internal", Weight: 1},
			{Protocol: "http", Address: addresses[1], HostHeader: "two.internal", Weight: 1},
		},
		OriginPolicy: edgeprotocol.OriginPolicyConfig{
			HealthURI: "/", TimeoutMS: 2000,
			ActiveHealth:  edgeprotocol.OriginActiveHealthConfig{Enabled: false},
			PassiveHealth: edgeprotocol.OriginPassiveHealthConfig{Enabled: false},
		},
	}
	if err := manager.ApplySite(config); err != nil {
		t.Fatalf("apply site: %v", err)
	}
	defer manager.Stop()

	bodies := map[string]bool{}
	for range 4 {
		request, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+"/", nil)
		request.Host = "hosts.example.test"
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		bodies[string(body)] = true
	}
	if !bodies["origin-1:one.internal"] || !bodies["origin-2:two.internal"] {
		t.Fatalf("per-origin Host headers were not preserved: %#v", bodies)
	}
}

func TestCacheConfigEnablesSiteScopedPurgeAPI(t *testing.T) {
	config := validHTTPConfig(t)
	config.Cache = enabledCachePolicy(t)
	encoded, err := renderCaddyConfig(map[string]SiteConfig{config.SiteID: config}, ":80", "node-host")
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	if !strings.Contains(text, `"basepath":"/__goveto/cache/`+config.SiteID+`"`) || !strings.Contains(text, `"enable":true`) {
		t.Fatalf("site-scoped Souin purge API missing from config: %s", text)
	}
}

func TestCacheConfigPassesPrivateDynamicLimitToSimpleFS(t *testing.T) {
	config := validHTTPConfig(t)
	config.Cache = enabledCachePolicy(t)
	encoded, err := renderCaddyConfig(
		map[string]SiteConfig{config.SiteID: config},
		":80",
		"node-host",
		NodeConfig{
			CacheDirectory:      "/var/cache/goveto",
			AutoMaxSize:         true,
			MaxDiskUsagePercent: 77,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, expected := range []string{
		`"path":"/var/cache/goveto/site-1"`,
		`"auto_max_size":true`,
		`"max_disk_usage_percent":77`,
		`"mode":"strict"`,
		`"disable_coalescing":true`,
		`"coalesce":true`,
		`"coalesce_headers":["Accept-Encoding","Range","If-Range"]`,
		`"cache_range_requests":true`,
		`"max_cacheable_body_bytes":67108864`,
		`"headers":["Accept-Encoding","Range","If-Range"]`,
		`"stale":"86400s"`,
		`"stale_while_revalidate_ttl":30`,
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("missing SimpleFS cache policy %s: %s", expected, text)
		}
	}
}

func TestDeliveryCORSRoutesRequireCrossOriginHeaders(t *testing.T) {
	site := SiteConfig{SiteID: "cors-site", Domains: []string{"cors.example.test"}}
	policy := cachepolicy.DefaultDeliveryPolicy()
	policy.CORS = cachepolicy.CORSConfig{Enabled: true, AllowOrigins: []string{"*"}, AllowMethods: []string{"GET", "OPTIONS"}}
	if err := policy.NormalizeAndValidate(); err != nil {
		t.Fatal(err)
	}

	preflight := routeByID(t, deliveryPreludeRoutes(site, policy), "site_cors-site_cors_preflight")
	preflightMatch := preflight["match"].([]any)[0].(map[string]any)
	preflightHeaders := preflightMatch["header"].(map[string][]string)
	if len(preflightHeaders["Origin"]) == 0 || len(preflightHeaders["Access-Control-Request-Method"]) == 0 {
		t.Fatalf("preflight route does not require CORS headers: %#v", preflightMatch)
	}

	actual := deliveryCORSRoute(site, policy)
	actualMatch := actual["match"].([]any)[0].(map[string]any)
	actualHeaders := actualMatch["header"].(map[string][]string)
	if values := actualHeaders["Origin"]; len(values) != 1 || values[0] != "*" {
		t.Fatalf("actual CORS route matches requests without Origin: %#v", actualMatch)
	}
}

func TestDeliveryProtocolFlagsRejectOtherUpgradesIndependently(t *testing.T) {
	ensureAgentLogSink(t)
	config := SiteConfig{
		SiteID: "site-1", Version: 1, Domains: []string{"example.com"},
		Listener: ListenerConfig{HTTPEnabled: true, HTTPPort: 18080},
		Origins:  []OriginConfig{{Protocol: "http", Address: "origin:80"}},
	}
	policy := cachepolicy.DefaultDeliveryPolicy()
	config.Delivery = toMap(t, policy)

	routes := deliveryPreludeRoutes(config, policy)
	rejection := routeByID(t, routes, "site_site-1_reject_upgrade")
	match := rejection["match"].([]any)[0].(map[string]any)
	if _, ok := match["not"]; !ok {
		t.Fatalf("WebSocket-only policy did not reject non-WebSocket upgrades: %#v", match)
	}

	policy.Protocols = cachepolicy.ProtocolConfig{HTTPUpgrade: true}
	routes = deliveryPreludeRoutes(config, policy)
	rejection = routeByID(t, routes, "site_site-1_reject_upgrade")
	match = rejection["match"].([]any)[0].(map[string]any)
	if _, ok := match["header_regexp"]; !ok {
		t.Fatalf("generic Upgrade policy did not reject WebSocket: %#v", match)
	}

	policy = cachepolicy.DefaultDeliveryPolicy()
	config.Delivery = toMap(t, policy)
	encoded, err := renderCaddyConfig(map[string]SiteConfig{config.SiteID: config}, ":80", "node-host")
	if err != nil {
		t.Fatal(err)
	}
	var caddyConfig caddy.Config
	if err = json.Unmarshal(caddy.RemoveMetaFields(encoded), &caddyConfig); err != nil {
		t.Fatal(err)
	}
	if err = caddy.Validate(&caddyConfig); err != nil {
		t.Fatalf("Caddy rejected independent Upgrade matchers: %v", err)
	}
}

func TestDeliverySplitRouteKeepsPoolPathConstraint(t *testing.T) {
	site := SiteConfig{SiteID: "split-site", Domains: []string{"split.example.test"}}
	policy := cachepolicy.DefaultDeliveryPolicy()
	policy.OriginPools = []cachepolicy.PathOriginPool{{
		Name: "api", Paths: []string{"/api/*"}, Origins: []cachepolicy.DeliveryOrigin{{Protocol: "http", Address: "api.internal:80"}},
	}}
	policy.Splits = []cachepolicy.TrafficSplitRule{{Name: "canary", Pool: "api", Percentage: 10}}
	if err := policy.NormalizeAndValidate(); err != nil {
		t.Fatal(err)
	}
	baseProxy := map[string]any{
		"load_balancing": map[string]any{},
		"transport":      map[string]any{"protocol": "goveto_http"},
	}
	routes, err := deliveryPoolRoutes(site, policy, nil, baseProxy, edgeprotocol.DefaultOriginPolicy())
	if err != nil {
		t.Fatal(err)
	}
	split := routeByID(t, routes, "site_split-site_split_0")
	match := split["match"].([]any)[0].(map[string]any)
	paths, ok := match["path"].([]string)
	if !ok || len(paths) != 1 || paths[0] != "/api/*" {
		t.Fatalf("split route lost pool path constraint: %#v", match)
	}
}

func TestDeliveryHTTPSPoolPreservesPrivateCAAndMTLS(t *testing.T) {
	_, _, caPEM := createTestCertificateAuthority(t)
	originPolicy := edgeprotocol.DefaultOriginPolicy()
	originPolicy.Transport.TLSRootCAPEM = []string{caPEM}
	originPolicy.Transport.TLSClientCertificatePEM = "client certificate"
	originPolicy.Transport.TLSClientPrivateKeyPEM = "client private key"
	baseProxy := map[string]any{
		"load_balancing": map[string]any{},
		"transport":      map[string]any{"protocol": "goveto_http"},
	}
	pool := cachepolicy.PathOriginPool{
		Name: "secure", Paths: []string{"/*"},
		Origins: []cachepolicy.DeliveryOrigin{{Protocol: "https", Address: "secure.internal:443", Weight: 1}},
	}
	proxy, err := deliveryProxy(baseProxy, pool, "site", originPolicy)
	if err != nil {
		t.Fatal(err)
	}
	transport := proxy["transport"].(map[string]any)
	tlsConfig := transport["tls"].(map[string]any)
	if _, ok := tlsConfig["ca"]; !ok {
		t.Fatalf("private CA was dropped from HTTPS pool transport: %#v", transport)
	}
	if transport["client_certificate_pem"] != "client certificate" || transport["client_private_key_pem"] != "client private key" {
		t.Fatalf("mTLS credentials were dropped from HTTPS pool transport: %#v", transport)
	}
}

func routeByID(t *testing.T, routes []any, id string) map[string]any {
	t.Helper()
	for _, raw := range routes {
		route, ok := raw.(map[string]any)
		if ok && route["@id"] == id {
			return route
		}
	}
	t.Fatalf("route %q not found in %#v", id, routes)
	return nil
}

func TestCompressionConfigRendersAllSettings(t *testing.T) {
	config := validHTTPConfig(t)
	policy := cachepolicy.DefaultCompressionPolicy()
	policy.Enabled = true
	policy.Recompress = true
	policy.MinimumLength = 2048
	policy.MaximumLength = 4 << 20
	policy.ExcludedPaths = []string{"/downloads", "/api/export"}
	config.Compression = toMap(t, policy)

	encoded, err := renderCaddyConfig(map[string]SiteConfig{config.SiteID: config}, ":80", "node-host")
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, expected := range []string{
		`"handler":"goveto_compression"`,
		`"recompress":true`,
		`"minimum_length":2048`,
		`"maximum_length":4194304`,
		`"excluded_paths":["/api/export","/downloads"]`,
		`"mime_types"`,
		`"excluded_extensions"`,
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("missing compression setting %s: %s", expected, text)
		}
	}
}

func TestCompressionConfigRejectsInvalidPolicy(t *testing.T) {
	config := validHTTPConfig(t)
	config.Compression = map[string]any{
		"enabled":        true,
		"extensions":     []string{"html"},
		"minimum_length": 1000,
		"maximum_length": 100,
	}
	if _, err := renderCaddyConfig(map[string]SiteConfig{config.SiteID: config}, ":80", "node-host"); err == nil {
		t.Fatal("expected invalid compression policy to fail")
	}
}

func TestSecurityConfigRendersWAFAndRateLimit(t *testing.T) {
	config := validHTTPConfig(t)
	waf := cachepolicy.DefaultWAFPolicy()
	waf.Enabled = true
	waf.Groups = []cachepolicy.WAFRuleGroup{{
		ID:       "admin",
		Enabled:  true,
		Operator: "AND",
		Action:   "BLOCK",
		Rules: []cachepolicy.WAFRequestRule{
			{Field: "PATH", Operator: "PREFIX", Value: "/admin"},
		},
	}}
	rateLimit := cachepolicy.RateLimitPolicy{Enabled: true, Rules: []cachepolicy.RateLimitRule{{
		ID: "cc", Enabled: true, Key: "CLIENT_IP", Requests: 20, WindowSeconds: 10,
	}}}
	config.WAF = toMap(t, waf)
	config.RateLimit = toMap(t, rateLimit)
	access := cachepolicy.DefaultAccessPolicy()
	access.Enabled = true
	access.IPBlocklist = []string{"192.0.2.0/24"}
	config.Access = toMap(t, access)

	encoded, err := renderCaddyConfig(map[string]SiteConfig{config.SiteID: config}, ":80", "node-host")
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, expected := range []string{`"handler":"goveto_waf"`, `"site_id":"site-1"`, `"SQL_INJECTION"`, `"window_seconds":10`, `"ip_blocklist":["192.0.2.0/24"]`} {
		if !strings.Contains(text, expected) {
			t.Fatalf("missing security policy %s: %s", expected, text)
		}
	}
}

func TestCaptchaConfigPassesPublishedChallengeSecret(t *testing.T) {
	config := validHTTPConfig(t)
	waf := cachepolicy.DefaultWAFPolicy()
	waf.Enabled = true
	waf.Presets = nil
	waf.Groups = []cachepolicy.WAFRuleGroup{{
		ID: "shield", Enabled: true, Operator: "AND", Action: cachepolicy.WAFActionCaptcha,
		Rules: []cachepolicy.WAFRequestRule{{Field: "PATH", Operator: "PREFIX", Value: "/"}},
	}}
	config.WAF = toMap(t, waf)
	config.WAF["challenge_secret"] = "published-secret"

	encoded, err := renderCaddyConfig(map[string]SiteConfig{config.SiteID: config}, ":80", "node-host")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"challenge_secret":"published-secret"`) {
		t.Fatalf("rendered config omitted CAPTCHA challenge secret: %s", encoded)
	}
}

func TestDeliveryConfigRendersHeadersRoutesPoolsAndSplits(t *testing.T) {
	ensureAgentLogSink(t)
	config := SiteConfig{
		SiteID: "delivery-site", Version: 1, Domains: []string{"delivery.example.test"},
		Listener: ListenerConfig{HTTPEnabled: true, HTTPPort: 8080},
		Origins:  []OriginConfig{{Protocol: "http", Address: "default-origin:80", Weight: 1}},
	}
	delivery := cachepolicy.DefaultDeliveryPolicy()
	delivery.RequestHeaders = []cachepolicy.HeaderRule{{Operation: "SET", Name: "X-Origin-Env", Value: "production"}}
	delivery.ResponseHeaders = []cachepolicy.HeaderRule{{Operation: "SET", Name: "X-Edge", Value: "goveto"}}
	delivery.Rewrites = []cachepolicy.RewriteRule{{Path: "/legacy/*", Replacement: "/current{http.request.uri.path}"}}
	delivery.Redirects = []cachepolicy.RedirectRule{{Path: "/moved", Location: "/new", Status: 308}}
	delivery.CORS = cachepolicy.CORSConfig{Enabled: true, AllowOrigins: []string{"https://app.example.test"}, AllowMethods: []string{"GET", "OPTIONS"}}
	delivery.Protocols = cachepolicy.ProtocolConfig{WebSocket: true, GRPC: true, HTTPUpgrade: true}
	delivery.ErrorPages = []cachepolicy.ErrorPage{{Statuses: []int{404, 502}, ContentType: "text/html", Body: "custom error"}}
	delivery.OriginPrefix = "/production"
	delivery.OriginPools = []cachepolicy.PathOriginPool{{
		Name: "api", Paths: []string{"/api/*"}, Scheduler: "weighted_round_robin",
		Origins: []cachepolicy.DeliveryOrigin{{Protocol: "https", Address: "api-origin:443", HostHeader: "api.internal", Weight: 3}},
	}}
	delivery.Splits = []cachepolicy.TrafficSplitRule{{Name: "canary", Pool: "api", CookieName: "cohort", Value: "canary", Percentage: 25}}
	if err := delivery.NormalizeAndValidate(); err != nil {
		t.Fatal(err)
	}
	config.Delivery = toMap(t, delivery)

	encoded, err := renderCaddyConfig(map[string]SiteConfig{config.SiteID: config}, ":8080", "node-host")
	if err != nil {
		t.Fatal(err)
	}
	raw := string(encoded)
	for _, expected := range []string{
		`"X-Origin-Env":["production"]`, `"X-Edge":["goveto"]`, `"handler":"rewrite"`,
		`"status_code":308`, `"Access-Control-Allow-Origin"`, `"versions":["1.1","2","h2c"]`,
		`"handle_response"`, `"uri":"/production{http.request.uri.path}?{http.request.uri.query}"`,
		`"path":["/api/*"]`, `"policy":"goveto_origin"`, `"goveto_split"`, `"cookie_name":"cohort"`,
	} {
		if !strings.Contains(raw, expected) {
			t.Fatalf("delivery config missing %s: %s", expected, raw)
		}
	}
	var caddyConfig caddy.Config
	if err = json.Unmarshal(caddy.RemoveMetaFields(encoded), &caddyConfig); err != nil {
		t.Fatal(err)
	}
	if err = caddy.Validate(&caddyConfig); err != nil {
		t.Fatalf("Caddy rejected delivery config: %v", err)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func validHTTPConfig(t *testing.T) SiteConfig {
	t.Helper()
	return SiteConfig{
		SiteID:  "site-1",
		Version: 1,
		Domains: []string{"example.com"},
		Listener: ListenerConfig{
			HTTPEnabled: true,
			HTTPPort:    freePort(t),
		},
		Origins: []OriginConfig{{Protocol: "http", Address: "origin:80"}},
	}
}

func enabledCachePolicy(t *testing.T) map[string]any {
	t.Helper()
	policy := cachepolicy.DefaultCachePolicy()
	policy.Enabled = true
	data, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err = json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func toMap(t *testing.T, value any) map[string]any {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err = json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func validHTTPSConfig(t *testing.T) SiteConfig {
	t.Helper()
	return SiteConfig{
		SiteID:  "site-https",
		Version: 3,
		Domains: []string{"secure.example.com"},
		Listener: ListenerConfig{
			HTTPEnabled:           true,
			HTTPPort:              freePort(t),
			RedirectHTTPToHTTPS:   true,
			HTTPSEnabled:          true,
			HTTPSPort:             freePort(t),
			HTTP2Enabled:          true,
			HTTP3Enabled:          true,
			TLSMinVersion:         "TLS1_3",
			HSTSEnabled:           true,
			HSTSMaxAge:            31536000,
			HSTSIncludeSubdomains: true,
			HSTSPreload:           true,
		},
		Certificates: []CertificateConfig{{CertificatePEM: "CERT", PrivateKeyPEM: "KEY"}},
		Origins:      []OriginConfig{{Protocol: "https", Address: "origin:443"}},
	}
}

func asStringSlice(value any) []string {
	items, _ := value.([]any)
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
