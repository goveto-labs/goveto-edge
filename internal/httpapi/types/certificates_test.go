package types

import (
	"encoding/json"
	"testing"
	"time"

	"goveto-edge/internal/storage/gen/model"
)

// TestNewCertificateSurfacesLifecycleState confirms the public certificate
// payload exposes the full issue/renew/publish lifecycle (status, expiry, and
// the per-stage error fields) without leaking encrypted material. These fields
// are what operators read to decide whether to roll back or reissue.
func TestNewCertificateSurfacesLifecycleState(t *testing.T) {
	notBefore := mustTime("2026-01-01T00:00:00Z")
	expires := mustTime("2026-04-01T00:00:00Z")
	lastRenewal := mustTime("2026-01-02T00:00:00Z")
	revokedAt := mustTime("2026-01-03T00:00:00Z")
	lastRevocation := mustTime("2026-01-03T00:01:00Z")
	renewError := "acme: rate limited"
	revocationError := "site rollout pending"
	revocationReason := 1
	issuer := "Let's Encrypt R3"
	algo := "RSA-2048"
	challenge := model.ACMEChallengeTypeHTTP_01
	directory := "https://acme-v02.api.letsencrypt.org/directory"

	cert := &model.Certificate{
		Id: "cert-1", Name: "edge", Source: model.CertificateSourceACME, Status: model.CertificateStatusACTIVE,
		DomainsJson: json.RawMessage(`["example.com","*.example.com"]`),
		NotBefore:   &notBefore, ExpiresAt: &expires, Issuer: &issuer, KeyAlgorithm: &algo,
		AcmeDirectoryUrl: &directory, AcmeChallengeType: &challenge, AutoRenew: true, RenewBeforeDays: 30,
		LastRenewalAttemptAt: &lastRenewal, LastRenewalError: &renewError,
		RevokedAt: &revokedAt, RevocationReason: &revocationReason,
		LastRevocationAttemptAt: &lastRevocation, LastRevocationError: &revocationError,
	}
	response := NewCertificate(cert)
	if response.ID != "cert-1" || response.Status != model.CertificateStatusACTIVE ||
		response.Source != model.CertificateSourceACME || !response.AutoRenew || response.RenewBeforeDays != 30 {
		t.Fatalf("lifecycle fields lost: %#v", response)
	}
	if len(response.Domains) != 2 || response.Domains[1] != "*.example.com" {
		t.Fatalf("domains not decoded: %#v", response.Domains)
	}
	if response.ExpiresAt == nil || !response.ExpiresAt.Equal(expires) {
		t.Fatalf("expiry not surfaced: %#v", response.ExpiresAt)
	}
	if response.LastRenewalError == nil || *response.LastRenewalError != renewError {
		t.Fatalf("renewal error not surfaced: %#v", response.LastRenewalError)
	}
	if response.ACMEChallengeType == nil || *response.ACMEChallengeType != challenge {
		t.Fatalf("challenge type not surfaced: %#v", response.ACMEChallengeType)
	}
	if response.RevokedAt == nil || !response.RevokedAt.Equal(revokedAt) ||
		response.RevocationReason == nil || *response.RevocationReason != revocationReason ||
		response.LastRevocationAttemptAt == nil || !response.LastRevocationAttemptAt.Equal(lastRevocation) ||
		response.LastRevocationError == nil || *response.LastRevocationError != revocationError {
		t.Fatalf("revocation fields lost: %#v", response)
	}
}

// TestNewCertificateToleratesMissingDomains confirms a certificate with no
// domain blob (e.g. a freshly created PENDING ACME order) decodes to an empty
// slice rather than panicking, so the state machine can render it pre-issue.
func TestNewCertificateToleratesMissingDomains(t *testing.T) {
	response := NewCertificate(&model.Certificate{
		Id: "cert-2", Source: model.CertificateSourceACME, Status: model.CertificateStatusPENDING,
	})
	if len(response.Domains) != 0 {
		t.Fatalf("nil domains blob must yield no domains, got %#v", response.Domains)
	}
}

// TestNewCertificateJobMapsOperationAndError confirms the job payload surfaces
// the operation (ISSUE/RENEW/REISSUE/REPUBLISH), status, attempts and the
// terminal error so an operator can distinguish a retried renewal from a
// permanently failed publish.
func TestNewCertificateJobMapsOperationAndError(t *testing.T) {
	jobError := "dns-01 challenge timed out"
	job := &model.CertificateJob{
		Id: "job-1", CertificateId: "cert-1", Operation: model.CertificateOperationREVOKE,
		Status: model.JobStatusFAILED, Attempts: 3, MaxAttempts: 5, Error: &jobError,
	}
	response := NewCertificateJob(job)
	if response.Operation != model.CertificateOperationREVOKE || response.Status != model.JobStatusFAILED ||
		response.Attempts != 3 || response.MaxAttempts != 5 || response.Error == nil || *response.Error != jobError {
		t.Fatalf("job lifecycle fields lost: %#v", response)
	}
}

func mustTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return parsed
}
