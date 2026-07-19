package edgeagent

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	cachepolicy "goveto-edge/internal/policy"

	"goveto-edge/caddy/agentlog"
)

type nopLogSink struct{}

func (nopLogSink) WriteCaddyLog([]byte) error { return nil }

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
	manager.SetAgentHost("550e8400-e29b-41d4-a716-446655440000")
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
	manager.SetAgentHost("550e8400-e29b-41d4-a716-446655440000")
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
	restored.SetAgentHost("550e8400-e29b-41d4-a716-446655440000")
	if err := restored.Restore(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restored.ConfigVersion() != 2 {
		t.Fatalf("restored config version: %d", restored.ConfigVersion())
	}
	_ = restored.Stop()
}

func TestHTTPSOriginRendersTLSTransport(t *testing.T) {
	config := validHTTPConfig(t)
	config.Origins[0].Protocol = "https"
	encoded, err := renderCaddyConfig(map[string]SiteConfig{config.SiteID: config}, ":80", "550e8400-e29b-41d4-a716-446655440000")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"transport":{"protocol":"http","tls":{}}`) {
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
	if len(routes) < 2 {
		t.Fatalf("expected agent + site routes, got %d", len(routes))
	}
	agentRoute := routes[0].(map[string]any)
	if agentRoute["@id"] != "goveto_agent_api" {
		t.Fatalf("agent route missing: %#v", agentRoute)
	}
	raw := string(encoded)
	if !strings.Contains(raw, `"policy":"client_ip_hash"`) {
		t.Fatalf("ip_hash was not mapped: %s", raw)
	}
	if !strings.Contains(raw, `"Host":["origin.internal"]`) {
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
		`"path":"/var/cache/goveto"`,
		`"auto_max_size":true`,
		`"max_disk_usage_percent":77`,
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("missing SimpleFS cache policy %s: %s", expected, text)
		}
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
