package edgecontrol

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"testing"
	"time"

	"goveto-edge/internal/node"
)

func TestAuthorityIssuesMutuallyAuthenticatedNodeCredential(t *testing.T) {
	authority := testAuthority(t)
	bundle, err := authority.IssueNode("550e8400-e29b-41d4-a716-446655440000")
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair([]byte(bundle.Certificate), []byte(bundle.PrivateKey))
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if CertificateNodeID(certificate) != bundle.NodeID {
		t.Fatalf("certificate node ID = %q", CertificateNodeID(certificate))
	}
	if len(certificate.ExtKeyUsage) != 1 || certificate.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth {
		t.Fatalf("unexpected extended key usage: %#v", certificate.ExtKeyUsage)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(bundle.CACertificate)) {
		t.Fatal("failed to load issued CA")
	}
	if _, err := certificate.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatal(err)
	}
	if bundle.Serial != certificate.SerialNumber.Text(16) {
		t.Fatalf("serial = %q, certificate = %q", bundle.Serial, certificate.SerialNumber.Text(16))
	}
}

func TestAuthorityIsStableAcrossControlPlaneReplicas(t *testing.T) {
	first := testAuthority(t)
	second := testAuthority(t)
	if string(first.caPEM) != string(second.caPEM) {
		t.Fatal("replicas derived different agent CAs")
	}
	firstServer, err := x509.ParseCertificate(first.serverCert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := firstServer.VerifyHostname("control.example"); err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(second.caPEM)
	if block == nil {
		t.Fatal("missing CA PEM block")
	}
}

func TestSignCSRRejectsMismatchedIdentity(t *testing.T) {
	authority := testAuthority(t)
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csr := credentialCSR(t, "550e8400-e29b-41d4-a716-446655440001", privateKey)
	if _, _, _, err := authority.SignCSR("550e8400-e29b-41d4-a716-446655440000", csr); err == nil {
		t.Fatal("expected mismatched CSR identity rejection")
	}
}

func TestSignCSRRejectsNonEd25519Key(t *testing.T) {
	authority := testAuthority(t)
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	csr := credentialCSR(t, "550e8400-e29b-41d4-a716-446655440000", privateKey)
	if _, _, _, err := authority.SignCSR("550e8400-e29b-41d4-a716-446655440000", csr); err == nil {
		t.Fatal("expected non-Ed25519 CSR rejection")
	}
}

func TestSignCSRRejectsInvalidPEM(t *testing.T) {
	authority := testAuthority(t)
	if _, _, _, err := authority.SignCSR("550e8400-e29b-41d4-a716-446655440000", "not-a-csr"); err == nil {
		t.Fatal("expected invalid CSR PEM rejection")
	}
}

func TestSignCSRIssuesClientCertificateForMatchingEd25519Request(t *testing.T) {
	authority := testAuthority(t)
	nodeID := "550e8400-e29b-41d4-a716-446655440000"
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csr := credentialCSR(t, nodeID, privateKey)
	certificatePEM, serial, notAfter, err := authority.SignCSR(nodeID, csr)
	if err != nil {
		t.Fatal(err)
	}
	if serial == "" || notAfter.Before(time.Now()) {
		t.Fatalf("unexpected serial/notAfter: %q %s", serial, notAfter)
	}
	block, _ := pem.Decode([]byte(certificatePEM))
	if block == nil {
		t.Fatal("missing certificate PEM")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if CertificateNodeID(certificate) != nodeID {
		t.Fatalf("issued cn = %q", CertificateNodeID(certificate))
	}
	if !certificate.PublicKey.(ed25519.PublicKey).Equal(privateKey.Public().(ed25519.PublicKey)) {
		t.Fatal("issued certificate public key does not match CSR")
	}
}

func credentialCSR(t *testing.T, nodeID string, privateKey any) string {
	t.Helper()
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: nodeID},
	}, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
}

func testAuthority(t *testing.T) *Authority {
	t.Helper()
	cipher, err := node.NewCredentialCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	authority, err := NewAuthority(cipher, "control.example:8443")
	if err != nil {
		t.Fatal(err)
	}
	return authority
}
