package certmanager

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"goveto-edge/internal/node"
	"goveto-edge/internal/storage/gen/model"
)

func TestCertificateRetryDelayUsesMinuteScale(t *testing.T) {
	if got := certificateRetryDelay(1); got != 2*time.Minute {
		t.Fatalf("first retry delay = %s", got)
	}
	if got := certificateRetryDelay(4); got != 16*time.Minute {
		t.Fatalf("fourth retry delay = %s", got)
	}
}

func TestParseRevocationReason(t *testing.T) {
	for input, want := range map[string]int{
		"key_compromise": 1,
		"SUPERSEDED":     4,
		"unspecified":    0,
	} {
		got, err := ParseRevocationReason(input)
		if err != nil || got != want {
			t.Fatalf("ParseRevocationReason(%q) = %d, %v, want %d", input, got, err, want)
		}
	}
	if _, err := ParseRevocationReason("delete_it"); err == nil {
		t.Fatal("invalid revocation reason was accepted")
	}
}

func TestValidateMaterialAndWildcardCoverage(t *testing.T) {
	now := time.Now().UTC()
	certificatePEM, privateKeyPEM := testCertificate(t, now, []string{"example.com", "*.example.com"}, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
	material, err := ValidateMaterial(certificatePEM, privateKeyPEM, now)
	if err != nil {
		t.Fatal(err)
	}
	if material.Fingerprint == "" || material.ExpiresAt.Before(now) {
		t.Fatalf("unexpected material: %#v", material)
	}
	if err = CoversDomains(material.Domains, []string{"example.com", "www.example.com"}); err != nil {
		t.Fatal(err)
	}
	if err = CoversDomains(material.Domains, []string{"deep.www.example.com"}); err == nil {
		t.Fatal("wildcard unexpectedly covered more than one label")
	}
}

func TestValidateMaterialRejectsClientOnlyCertificate(t *testing.T) {
	now := time.Now().UTC()
	certificatePEM, privateKeyPEM := testCertificate(t, now, []string{"example.com"}, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})
	if _, err := ValidateMaterial(certificatePEM, privateKeyPEM, now); err == nil {
		t.Fatal("client-only certificate was accepted")
	}
}

func TestPrivateKeyEnvelopeIsScoped(t *testing.T) {
	cipher, err := node.NewCredentialCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := EncryptPrivateKey(cipher, "cluster-a", "cert-a", "secret")
	if err != nil {
		t.Fatal(err)
	}
	certificate := &model.Certificate{Id: "cert-a", ClusterId: "cluster-a", PrivateKeyEncrypted: encrypted}
	plain, err := DecryptPrivateKey(cipher, certificate)
	if err != nil || plain != "secret" {
		t.Fatalf("plain=%q err=%v", plain, err)
	}
	certificate.ClusterId = "cluster-b"
	if _, err = DecryptPrivateKey(cipher, certificate); err == nil {
		t.Fatal("ciphertext decrypted in another cluster scope")
	}
}

func TestRewrapPrivateKeyUpgradesLegacyEnvelope(t *testing.T) {
	oldKey := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	newKey := base64.StdEncoding.EncodeToString([]byte("abcdef0123456789abcdef0123456789"))
	oldCipher, err := node.NewCredentialCipher(oldKey)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := EncryptPrivateKey(oldCipher, "cluster-a", "cert-a", "secret")
	if err != nil {
		t.Fatal(err)
	}
	legacy := legacyPrivateKeyEnvelope + strings.TrimPrefix(encrypted, "enc:v2:"+oldCipher.KeyID()+":")
	rotated, err := node.NewCredentialCipherKeyring(newKey, oldKey)
	if err != nil {
		t.Fatal(err)
	}
	certificate := &model.Certificate{Id: "cert-a", ClusterId: "cluster-a", PrivateKeyEncrypted: legacy}
	rewrapped, changed, err := RewrapPrivateKey(rotated, certificate)
	if err != nil || !changed || !rotated.IsCurrent(rewrapped) {
		t.Fatalf("RewrapPrivateKey() = %q, %v, %v", rewrapped, changed, err)
	}
	certificate.PrivateKeyEncrypted = rewrapped
	plain, err := DecryptPrivateKey(rotated, certificate)
	if err != nil || plain != "secret" {
		t.Fatalf("DecryptPrivateKey() = %q, %v", plain, err)
	}
}

func testCertificate(t *testing.T, now time.Time, domains []string, usages []x509.ExtKeyUsage) (string, string) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(100), Subject: pkix.Name{CommonName: "Test Root"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(48 * time.Hour), IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, caKey.Public(), caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: domains[0]}, DNSNames: domains,
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: usages,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, key.Public(), caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	chain := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})...)
	return string(chain), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
}
