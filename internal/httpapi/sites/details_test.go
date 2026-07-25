package sites

import (
	"testing"

	"goveto-edge/internal/edgeprotocol"
)

func TestRedactOriginPolicyRemovesMTLSPrivateKey(t *testing.T) {
	policy := edgeprotocol.DefaultOriginPolicy()
	policy.Transport.TLSClientCertificatePEM = "certificate"
	policy.Transport.TLSClientPrivateKeyPEM = "private key"
	redacted := redactOriginPolicy(policy)
	if redacted.Transport.TLSClientPrivateKeyPEM != "" || redacted.Transport.TLSClientCertificatePEM != "" {
		t.Fatal("mTLS credential was exposed")
	}
	if !redacted.Transport.MTLSConfigured || policy.Transport.TLSClientPrivateKeyPEM != "private key" {
		t.Fatal("redaction lost configured state or mutated the source")
	}
}

func TestPreserveOriginMTLSKeepsRedactedCredential(t *testing.T) {
	current := edgeprotocol.DefaultOriginPolicy()
	current.Transport.TLSClientCertificatePEM = "certificate"
	current.Transport.TLSClientPrivateKeyPEM = "private key"
	candidate := edgeprotocol.DefaultOriginPolicy()
	candidate.Transport.MTLSConfigured = true
	merged := preserveOriginMTLS(candidate, current)
	if merged.Transport.TLSClientCertificatePEM != "certificate" || merged.Transport.TLSClientPrivateKeyPEM != "private key" {
		t.Fatalf("mTLS credential was not preserved: %#v", merged.Transport)
	}
}
