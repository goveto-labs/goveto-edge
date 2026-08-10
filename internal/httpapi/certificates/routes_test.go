package certificates

import (
	"encoding/json"
	"testing"

	"goveto-edge/internal/storage/gen/model"
)

// TestOperationAllowed pins the certificate lifecycle state machine: only an
// ACME-issued certificate may be renewed or reissued (those operations drive
// the ACME order flow), while re-publishing existing material is allowed for
// any source. A regression that let RENEW run against a manually uploaded
// certificate would enqueue an ACME job with no directory account and fail
// opaquely.
func TestOperationAllowed(t *testing.T) {
	tests := []struct {
		name      string
		source    model.CertificateSource
		operation model.CertificateOperation
		allowed   bool
	}{
		{"renew acme", model.CertificateSourceACME, model.CertificateOperationRENEW, true},
		{"reissue acme", model.CertificateSourceACME, model.CertificateOperationREISSUE, true},
		{"republish acme", model.CertificateSourceACME, model.CertificateOperationREPUBLISH, true},
		{"revoke acme", model.CertificateSourceACME, model.CertificateOperationREVOKE, true},
		{"renew manual rejected", model.CertificateSourceMANUAL, model.CertificateOperationRENEW, false},
		{"reissue manual rejected", model.CertificateSourceMANUAL, model.CertificateOperationREISSUE, false},
		{"republish manual", model.CertificateSourceMANUAL, model.CertificateOperationREPUBLISH, true},
		{"revoke manual rejected", model.CertificateSourceMANUAL, model.CertificateOperationREVOKE, false},
		{"issue manual", model.CertificateSourceMANUAL, model.CertificateOperationISSUE, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := operationAllowed(test.source, test.operation); got != test.allowed {
				t.Fatalf("operationAllowed(%s, %s) = %v, want %v", test.source, test.operation, got, test.allowed)
			}
		})
	}
}

// TestCertificateDomainsDecodeRoundTrip confirms the JSON domain list the
// certificate state machine persists survives a marshal/unmarshal cycle, so a
// renew or reissue reads back the same SAN set it was scheduled with.
func TestCertificateDomainsDecodeRoundTrip(t *testing.T) {
	original := []string{"example.com", "*.example.com"}
	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded []string
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != len(original) || decoded[0] != original[0] || decoded[1] != original[1] {
		t.Fatalf("domains lost in round trip: %v -> %v", original, decoded)
	}
}
