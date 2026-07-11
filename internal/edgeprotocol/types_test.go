package edgeprotocol

import "testing"

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
