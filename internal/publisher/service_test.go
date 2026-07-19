package publisher

import (
	"testing"

	"goveto-edge/internal/edgeprotocol"
)

func TestSuccessfulTargetsPartitionsResults(t *testing.T) {
	targets := []target{{NodeID: "a"}, {NodeID: "b"}, {NodeID: "c"}}
	results := []targetResult{{NodeID: "a", Success: true}, {NodeID: "b", Error: "timeout"}, {NodeID: "c", Success: true}}

	succeeded, failed := successfulTargets(results, targets)
	if len(succeeded) != 2 || succeeded[0].NodeID != "a" || succeeded[1].NodeID != "c" {
		t.Fatalf("unexpected successful targets: %#v", succeeded)
	}
	if len(failed) != 1 || failed[0].NodeID != "b" || failed[0].Error != "timeout" {
		t.Fatalf("unexpected failed results: %#v", failed)
	}
}

func TestAllSucceeded(t *testing.T) {
	tests := []struct {
		name    string
		results []targetResult
		want    bool
	}{
		{name: "empty rollback", want: true},
		{name: "complete", results: []targetResult{{Success: true}, {Success: true}}, want: true},
		{name: "partial", results: []targetResult{{Success: true}, {Success: false}}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := allSucceeded(test.results); got != test.want {
				t.Fatalf("allSucceeded() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestNormalizeListenerWithoutCertificatesFallsBackToHTTP(t *testing.T) {
	config := edgeprotocol.SiteConfig{
		SiteID:  "site-http",
		Version: 1,
		Domains: []string{"example.test"},
		Origins: []edgeprotocol.OriginConfig{{Protocol: "http", Address: "origin.test:80"}},
		Listener: edgeprotocol.ListenerConfig{
			HTTPPort:              80,
			RedirectHTTPToHTTPS:   true,
			HTTPSEnabled:          true,
			HTTPSPort:             443,
			HTTP2Enabled:          true,
			HTTP3Enabled:          true,
			HSTSEnabled:           true,
			HSTSIncludeSubdomains: true,
			HSTSPreload:           true,
			OCSPStaplingEnabled:   true,
		},
	}

	normalizeListenerForCertificates(&config)

	if !config.Listener.HTTPEnabled || config.Listener.HTTPSEnabled || config.Listener.RedirectHTTPToHTTPS {
		t.Fatalf("expected HTTP-only listener, got %#v", config.Listener)
	}
	if config.Listener.HTTP2Enabled || config.Listener.HTTP3Enabled || config.Listener.HSTSEnabled || config.Listener.OCSPStaplingEnabled {
		t.Fatalf("HTTPS-only features remain enabled: %#v", config.Listener)
	}
	if err := config.Validate(); err != nil {
		t.Fatalf("HTTP fallback config should be accepted by the agent: %v", err)
	}
}

func TestNormalizeListenerWithCertificatePreservesHTTPS(t *testing.T) {
	config := edgeprotocol.SiteConfig{
		Listener:     edgeprotocol.ListenerConfig{HTTPSEnabled: true, RedirectHTTPToHTTPS: true},
		Certificates: []edgeprotocol.CertificateConfig{{CertificatePEM: "cert", PrivateKeyPEM: "key"}},
	}

	normalizeListenerForCertificates(&config)

	if !config.Listener.HTTPSEnabled || !config.Listener.RedirectHTTPToHTTPS {
		t.Fatalf("HTTPS listener was unexpectedly changed: %#v", config.Listener)
	}
}
