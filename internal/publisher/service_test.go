package publisher

import (
	"errors"
	"testing"

	"goveto-edge/internal/edgeprotocol"
	"goveto-edge/internal/node"
	"goveto-edge/internal/storage/gen/model"
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

func TestWAFChallengeSecretIsStableAndSiteScoped(t *testing.T) {
	cipher, err := node.NewCredentialCipher("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	if err != nil {
		t.Fatal(err)
	}
	one := wafChallengeSecret(cipher, "site-1")
	if one != wafChallengeSecret(cipher, "site-1") || one == wafChallengeSecret(cipher, "site-2") {
		t.Fatal("WAF challenge secret is not stable and site-scoped")
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

func TestPublishOutcomeDoesNotRetryAfterRollback(t *testing.T) {
	result := []targetResult{{NodeID: "node-1", Success: true, RolledBack: true}}
	outcome := publishOutcome(model.JobStatusFAILED, result, errors.New("publish rejected"), true)
	if outcome.Retryable || outcome.Compensation == nil || outcome.Err == nil {
		t.Fatalf("rollback outcome = %#v", outcome)
	}
}

func TestNextPublishVersionHandlesMissingHistory(t *testing.T) {
	if got := nextPublishVersion(1, nil, nil); got != 2 {
		t.Fatalf("next version = %d, want 2", got)
	}
	pending := &model.PublishJob{Version: 7}
	if got := nextPublishVersion(2, pending, nil); got != 7 {
		t.Fatalf("pending version = %d, want 7", got)
	}
	latest := &model.ConfigVersion{Version: 9}
	if got := nextPublishVersion(3, nil, latest); got != 10 {
		t.Fatalf("version after history = %d, want 10", got)
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
