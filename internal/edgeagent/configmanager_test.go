package edgeagent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplySitePersistenceFailureDoesNotAdvanceVersion(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parent, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	manager := NewConfigManager(filepath.Join(parent, "sites.json"), ":0")
	config := validHTTPConfig()
	first := manager.ApplySite(config)
	second := manager.ApplySite(config)
	if first == nil || second == nil {
		t.Fatal("expected persistence failures")
	}
	if strings.Contains(second.Error(), "version is not newer") {
		t.Fatalf("version was advanced after persistence failure: %v", second)
	}
	if manager.ConfigVersion() != 0 {
		t.Fatalf("config version advanced after failure: %d", manager.ConfigVersion())
	}
}

func TestHTTPSOriginRendersTLSTransport(t *testing.T) {
	config := validHTTPConfig()
	config.Origins[0].Protocol = "https"
	encoded, err := renderCaddyConfig(map[string]SiteConfig{config.SiteID: config}, ":80", "550e8400-e29b-41d4-a716-446655440000")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"transport":{"protocol":"http","tls":{}}`) {
		t.Fatalf("HTTPS transport missing: %s", encoded)
	}
}

func TestMixedOriginProtocolsRejected(t *testing.T) {
	config := validHTTPConfig()
	config.Origins = append(config.Origins, OriginConfig{Protocol: "https", Address: "origin-2:443"})
	if err := config.Validate(); err == nil {
		t.Fatal("expected mixed protocols to be rejected")
	}
}

func validHTTPConfig() SiteConfig {
	return SiteConfig{SiteID: "site-1", Version: 1, Domains: []string{"example.com"}, Listener: ListenerConfig{HTTPEnabled: true, HTTPPort: 8080}, Origins: []OriginConfig{{Protocol: "http", Address: "origin:80"}}}
}
