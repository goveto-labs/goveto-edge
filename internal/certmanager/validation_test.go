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
	"testing"
	"time"

	"goveto-edge/internal/node"
	"goveto-edge/internal/storage/gen/model"
)

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
	certificate := &model.Certificate{Id: "cert-a", ClusterId: "cluster-a", PrivateKeyEncrypted: &encrypted}
	plain, err := DecryptPrivateKey(cipher, certificate)
	if err != nil || plain != "secret" {
		t.Fatalf("plain=%q err=%v", plain, err)
	}
	certificate.ClusterId = "cluster-b"
	if _, err = DecryptPrivateKey(cipher, certificate); err == nil {
		t.Fatal("ciphertext decrypted in another cluster scope")
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
