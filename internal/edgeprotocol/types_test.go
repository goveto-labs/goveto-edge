package edgeprotocol

import (
	"encoding/json"
	"testing"
)

func TestSiteConfigValidateRequiredFields(t *testing.T) {
	if err := (SiteConfig{}).Validate(); err == nil {
		t.Fatal("expected required fields error")
	}
}

func TestSiteConfigValidatePortsAndRedirect(t *testing.T) {
	config := SiteConfig{
		SiteID:  "site",
		Version: 1,
		Domains: []string{"example.com"},
		Listener: ListenerConfig{
			HTTPEnabled:         true,
			HTTPPort:            0,
			HTTPSEnabled:        true,
			HTTPSPort:           443,
			RedirectHTTPToHTTPS: true,
		},
		Certificates: []CertificateConfig{{CertificatePEM: "c", PrivateKeyPEM: "k"}},
		Origins:      []OriginConfig{{Protocol: "http", Address: "origin:80"}},
	}
	if err := config.Validate(); err == nil {
		t.Fatal("expected invalid HTTP port")
	}
	config.Listener.HTTPPort = 80
	config.Listener.HTTPEnabled = false
	if err := config.Validate(); err == nil {
		t.Fatal("expected redirect requires both protocols")
	}
}

func TestSiteConfigValidateHTTPSRequiresCertificate(t *testing.T) {
	config := SiteConfig{
		SiteID:   "site",
		Version:  1,
		Domains:  []string{"example.com"},
		Listener: ListenerConfig{HTTPSEnabled: true, HTTPSPort: 443},
		Origins:  []OriginConfig{{Protocol: "https", Address: "origin:443"}},
	}
	if err := config.Validate(); err == nil {
		t.Fatal("expected certificate requirement")
	}
}

func TestSiteConfigValidateSchedulerAndOrigin(t *testing.T) {
	config := SiteConfig{
		SiteID:    "site",
		Version:   1,
		Domains:   []string{"example.com"},
		Listener:  ListenerConfig{HTTPEnabled: true, HTTPPort: 80},
		Origins:   []OriginConfig{{Protocol: "ftp", Address: "origin:21"}},
		Scheduler: "weird",
	}
	if err := config.Validate(); err == nil {
		t.Fatal("expected invalid origin protocol")
	}
	config.Origins[0].Protocol = "http"
	if err := config.Validate(); err == nil {
		t.Fatal("expected unsupported scheduler")
	}
	config.Scheduler = "round_robin"
	if err := config.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestSiteConfigValidateMixedOrigins(t *testing.T) {
	config := SiteConfig{
		SiteID:   "site",
		Version:  1,
		Domains:  []string{"example.com"},
		Listener: ListenerConfig{HTTPEnabled: true, HTTPPort: 80},
		Origins: []OriginConfig{
			{Protocol: "http", Address: "a:80"},
			{Protocol: "https", Address: "b:443"},
		},
	}
	if err := config.Validate(); err == nil {
		t.Fatal("expected mixed origin rejection")
	}
}

func TestDecodeOriginPolicyMergesLegacyColumnsAndGovernance(t *testing.T) {
	governance := json.RawMessage(`{
		"timeout_ms":1,
		"active_health":{"enabled":true,"method":"HEAD","expected_status":204,"fails":4},
		"passive_health":{"enabled":true,"max_fails":5},
		"transport":{"ip_version":"ipv4","tls_server_name":"origin.internal"},
		"retry":{"retries":4,"try_duration_ms":8000,"try_interval_ms":400}
	}`)
	policy, err := DecodeOriginPolicy(governance, json.RawMessage(`{"X-Origin":"pool"}`), 12000)
	if err != nil {
		t.Fatal(err)
	}
	if policy.TimeoutMS != 12000 || policy.Headers["X-Origin"][0] != "pool" {
		t.Fatalf("legacy columns did not override governance: %#v", policy)
	}
	if policy.ActiveHealth.Method != "HEAD" || policy.ActiveHealth.ExpectedStatus != 204 || policy.ActiveHealth.Fails != 4 {
		t.Fatalf("active health policy lost: %#v", policy.ActiveHealth)
	}
	if policy.ActiveHealth.Enabled {
		t.Fatal("decoded active health probing must be disabled")
	}
	if policy.ActiveHealth.IntervalMS != 30000 || policy.PassiveHealth.FailDurationMS != 30000 {
		t.Fatalf("missing governance defaults: %#v", policy)
	}
	if policy.Transport.IPVersion != "ipv4" || policy.Transport.TLSServerName != "origin.internal" || policy.Retry.Retries != 4 {
		t.Fatalf("transport/retry policy lost: %#v", policy)
	}
}

func TestValidateOriginPolicyRejectsUnsafeValues(t *testing.T) {
	policy := DefaultOriginPolicy()
	policy.Transport.IPVersion = "prefer_magic"
	if err := ValidateOriginPolicy(policy); err == nil {
		t.Fatal("invalid IP policy was accepted")
	}
	policy = DefaultOriginPolicy()
	policy.Transport.TLSClientCertificatePEM = "certificate"
	if err := ValidateOriginPolicy(policy); err == nil {
		t.Fatal("incomplete mTLS pair was accepted")
	}
	policy = DefaultOriginPolicy()
	policy.Headers = map[string][]string{"Bad Header": {"value"}}
	if err := ValidateOriginPolicy(policy); err == nil {
		t.Fatal("invalid origin header was accepted")
	}
}

func TestDefaultOriginPolicyUsesRequestDrivenTransportHealth(t *testing.T) {
	policy := DefaultOriginPolicy()
	if policy.ActiveHealth.Enabled {
		t.Fatal("active origin probing must be disabled by default")
	}
	if !policy.PassiveHealth.Enabled {
		t.Fatal("passive transport failure health must remain enabled")
	}
	if len(policy.PassiveHealth.UnhealthyStatus) != 0 || policy.PassiveHealth.UnhealthyLatencyMS != 0 {
		t.Fatalf("HTTP responses must not trip health: %#v", policy.PassiveHealth)
	}
}

func TestNormalizeOriginPolicyDropsResponseBasedHealthFailures(t *testing.T) {
	policy := DefaultOriginPolicy()
	policy.ActiveHealth.Enabled = true
	policy.PassiveHealth.UnhealthyStatus = []int{404, 500, 503}
	policy.PassiveHealth.UnhealthyLatencyMS = 2500
	policy.PassiveHealth.UnhealthyRequestCount = 64
	policy = NormalizeOriginPolicy(policy)
	if policy.ActiveHealth.Enabled || len(policy.PassiveHealth.UnhealthyStatus) != 0 ||
		policy.PassiveHealth.UnhealthyLatencyMS != 0 || policy.PassiveHealth.UnhealthyRequestCount != 0 {
		t.Fatalf("non-transport health survived normalization: %#v", policy)
	}
}

func TestPurgeRequestValidate(t *testing.T) {
	valid := []PurgeRequest{
		{SiteID: "site", Type: "URL", Values: []string{"/index.html"}},
		{SiteID: "site", Type: "PREFIX", Values: []string{"/assets/"}},
		{SiteID: "site", Type: "TAG", Values: []string{"product-42"}},
		{SiteID: "site", Type: "ALL"},
	}
	for _, request := range valid {
		if err := request.Validate(); err != nil {
			t.Fatalf("Validate(%+v): %v", request, err)
		}
	}
	invalid := []PurgeRequest{{Type: "ALL"}, {SiteID: "site", Type: "URL"}, {SiteID: "site", Type: "ALL", Values: []string{"unexpected"}}, {SiteID: "site", Type: "UNKNOWN"}}
	for _, request := range invalid {
		if err := request.Validate(); err == nil {
			t.Fatalf("Validate(%+v) unexpectedly succeeded", request)
		}
	}
}
