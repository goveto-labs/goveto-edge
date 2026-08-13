package edgecontrol

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/credentials"

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
	if !bytes.Equal(first.runtime.Load().serverCert.Certificate[0], second.runtime.Load().serverCert.Certificate[0]) {
		t.Fatal("replicas derived different gateway certificates")
	}
	firstServer, err := x509.ParseCertificate(first.runtime.Load().serverCert.Certificate[0])
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

func TestAuthorityUpdatesGatewayWithoutRestartingTLSConfig(t *testing.T) {
	authority := testAuthority(t)
	tlsConfig := authority.ServerTLSConfig()
	initialBundle, err := authority.IssueNode("550e8400-e29b-41d4-a716-446655440000")
	if err != nil {
		t.Fatal(err)
	}
	assertAuthorityTLSHandshake(t, tlsConfig, initialBundle, "control.example")

	if err := authority.UpdateGatewayAddress("agents.example.net:9443"); err != nil {
		t.Fatal(err)
	}
	bundle, err := authority.IssueNode("550e8400-e29b-41d4-a716-446655440000")
	if err != nil {
		t.Fatal(err)
	}
	if bundle.GatewayAddress != "agents.example.net:9443" || bundle.ServerName != "agents.example.net" {
		t.Fatalf("updated identity endpoint = %q / %q", bundle.GatewayAddress, bundle.ServerName)
	}
	assertAuthorityTLSHandshake(t, tlsConfig, bundle, bundle.ServerName)
	assertAuthorityTLSHandshakeFails(t, tlsConfig, bundle, "control.example")
}

func TestPreparedGatewayUpdateIsNotVisibleUntilApplied(t *testing.T) {
	authority := testAuthority(t)
	apply, err := authority.PrepareGatewayAddressUpdate("agents.example.net:9443")
	if err != nil {
		t.Fatal(err)
	}

	before, err := authority.IssueNode("550e8400-e29b-41d4-a716-446655440000")
	if err != nil {
		t.Fatal(err)
	}
	if before.GatewayAddress != "control.example:8443" {
		t.Fatalf("prepared update became visible early: %q", before.GatewayAddress)
	}

	apply()
	after, err := authority.IssueNode("550e8400-e29b-41d4-a716-446655440000")
	if err != nil {
		t.Fatal(err)
	}
	if after.GatewayAddress != "agents.example.net:9443" || after.ServerName != "agents.example.net" {
		t.Fatalf("applied identity endpoint = %q / %q", after.GatewayAddress, after.ServerName)
	}
}

func TestInvalidGatewayUpdateKeepsCurrentRuntime(t *testing.T) {
	authority := testAuthority(t)
	if err := authority.UpdateGatewayAddress(":9443"); err == nil {
		t.Fatal("expected empty gateway host to be rejected")
	}
	bundle, err := authority.IssueNode("550e8400-e29b-41d4-a716-446655440000")
	if err != nil {
		t.Fatal(err)
	}
	if bundle.GatewayAddress != "control.example:8443" || bundle.ServerName != "control.example" {
		t.Fatalf("failed update changed identity endpoint = %q / %q", bundle.GatewayAddress, bundle.ServerName)
	}
}

func TestConcurrentGatewayUpdatesKeepIdentityEndpointConsistent(t *testing.T) {
	authority := testAuthority(t)
	endpoints := []struct {
		address string
		name    string
	}{
		{address: "agents-a.example.net:8443", name: "agents-a.example.net"},
		{address: "agents-b.example.net:9443", name: "agents-b.example.net"},
	}

	var wait sync.WaitGroup
	errors := make(chan error, 64)
	for worker := 0; worker < 8; worker++ {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			for iteration := 0; iteration < 25; iteration++ {
				endpoint := endpoints[(worker+iteration)%len(endpoints)]
				if err := authority.UpdateGatewayAddress(endpoint.address); err != nil {
					errors <- err
					return
				}
				bundle, err := authority.IssueNode(fmt.Sprintf("node-%d-%d", worker, iteration))
				if err != nil {
					errors <- err
					return
				}
				matched := false
				for _, candidate := range endpoints {
					if bundle.GatewayAddress == candidate.address && bundle.ServerName == candidate.name {
						matched = true
						break
					}
				}
				if !matched {
					errors <- fmt.Errorf("torn identity endpoint %q / %q", bundle.GatewayAddress, bundle.ServerName)
					return
				}
			}
		}(worker)
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
}

func assertAuthorityTLSHandshake(t *testing.T, serverConfig *tls.Config, bundle CredentialBundle, serverName string) {
	t.Helper()
	if err := authorityTLSHandshake(serverConfig, bundle, serverName); err != nil {
		t.Fatalf("TLS handshake for %q failed: %v", serverName, err)
	}
}

func assertAuthorityTLSHandshakeFails(t *testing.T, serverConfig *tls.Config, bundle CredentialBundle, serverName string) {
	t.Helper()
	if err := authorityTLSHandshake(serverConfig, bundle, serverName); err == nil {
		t.Fatalf("TLS handshake for stale server name %q succeeded", serverName)
	}
}

func authorityTLSHandshake(serverConfig *tls.Config, bundle CredentialBundle, serverName string) error {
	clientCertificate, err := tls.X509KeyPair([]byte(bundle.Certificate), []byte(bundle.PrivateKey))
	if err != nil {
		return err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(bundle.CACertificate)) {
		return errors.New("load authority CA")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()

	serverCredentials := credentials.NewTLS(serverConfig)
	serverResult := make(chan error, 1)
	go func() {
		serverConn, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverResult <- acceptErr
			return
		}
		defer serverConn.Close()
		_ = serverConn.SetDeadline(time.Now().Add(5 * time.Second))
		securedConn, _, handshakeErr := serverCredentials.ServerHandshake(serverConn)
		if securedConn != nil {
			defer securedConn.Close()
		}
		serverResult <- handshakeErr
	}()
	clientConn, err := net.DialTimeout("tcp", listener.Addr().String(), 5*time.Second)
	if err != nil {
		return err
	}
	defer clientConn.Close()
	_ = clientConn.SetDeadline(time.Now().Add(5 * time.Second))
	clientErr := tls.Client(clientConn, &tls.Config{
		MinVersion:   tls.VersionTLS13,
		ServerName:   serverName,
		RootCAs:      roots,
		Certificates: []tls.Certificate{clientCertificate},
		NextProtos:   []string{"h2"},
	}).Handshake()
	if clientErr != nil {
		clientConn.Close()
	}
	serverErr := <-serverResult
	if clientErr != nil {
		return clientErr
	}
	return serverErr
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

func TestIndependentCAKeyPreservesLegacyTrustRoot(t *testing.T) {
	cipher, err := node.NewCredentialCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := NewAuthority(cipher, "control.example:8443")
	if err != nil {
		t.Fatal(err)
	}
	caKey := base64.StdEncoding.EncodeToString(cipher.Derive("goveto-edge/agent-mtls/ca/v1"))
	separated, err := NewAuthorityWithCAKey(caKey, "control.example:8443")
	if err != nil {
		t.Fatal(err)
	}
	if string(legacy.caPEM) != string(separated.caPEM) {
		t.Fatal("separating the CA key changed the existing trust root")
	}
}

func TestPinAgentCADetectsImplicitRotation(t *testing.T) {
	dir := t.TempDir()
	ca := func(seed byte) *Authority {
		t.Helper()
		encoded := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{seed}, ed25519.SeedSize))
		authority, err := NewAuthorityWithCAKey(encoded, "control.example:8443")
		if err != nil {
			t.Fatal(err)
		}
		return authority
	}
	original := ca(1)

	// First boot records the fingerprint.
	if err := PinAgentCA(dir, original, false); err != nil {
		t.Fatalf("first boot: %v", err)
	}
	// Same CA on reboot is a no-op.
	if err := PinAgentCA(dir, original, false); err != nil {
		t.Fatalf("reboot: %v", err)
	}

	// Implicit (unpinned) rotation must fail-fast to avoid orphaning agents.
	rotated := ca(2)
	if err := PinAgentCA(dir, rotated, false); err == nil {
		t.Fatal("expected failure when implicitly derived CA changed")
	}

	// Explicitly pinned rotation is allowed and updates the recorded fingerprint.
	if err := PinAgentCA(dir, rotated, true); err != nil {
		t.Fatalf("pinned rotation should be allowed: %v", err)
	}
	if err := PinAgentCA(dir, rotated, false); err != nil {
		t.Fatalf("reboot after pinned rotation: %v", err)
	}
}
