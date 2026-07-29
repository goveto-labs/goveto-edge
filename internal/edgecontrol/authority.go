package edgecontrol

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strings"
	"time"

	"goveto-edge/internal/node"
)

const nodeCertificateValidity = 30 * 24 * time.Hour

type CredentialBundle struct {
	NodeID         string    `json:"node_id"`
	GatewayAddress string    `json:"gateway_address"`
	ServerName     string    `json:"server_name"`
	CACertificate  string    `json:"ca_certificate_pem"`
	Certificate    string    `json:"certificate_pem"`
	PrivateKey     string    `json:"private_key_pem"`
	Serial         string    `json:"-"`
	NotAfter       time.Time `json:"-"`
}

type Authority struct {
	ca             *x509.Certificate
	caKey          ed25519.PrivateKey
	caPEM          []byte
	gatewayAddress string
	serverName     string
	serverCert     tls.Certificate
}

func NewAuthority(cipher *node.CredentialCipher, gatewayAddress string) (*Authority, error) {
	host, _, err := net.SplitHostPort(gatewayAddress)
	if err != nil || strings.TrimSpace(host) == "" {
		return nil, fmt.Errorf("agent gateway public address must be host:port: %w", err)
	}

	caKey := ed25519.NewKeyFromSeed(cipher.Derive("goveto-edge/agent-mtls/ca/v1")[:ed25519.SeedSize])
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Goveto Edge Agent CA"},
		NotBefore:             time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:              time.Date(2125, 1, 1, 0, 0, 0, 0, time.UTC),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, caKey.Public(), caKey)
	if err != nil {
		return nil, fmt.Errorf("create agent CA: %w", err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, err
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})

	serverKey := ed25519.NewKeyFromSeed(cipher.Derive("goveto-edge/agent-mtls/server/v1\x00" + host)[:ed25519.SeedSize])
	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    caTemplate.NotBefore,
		NotAfter:     caTemplate.NotAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		serverTemplate.IPAddresses = []net.IP{ip}
	} else {
		serverTemplate.DNSNames = []string{host}
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, ca, serverKey.Public(), caKey)
	if err != nil {
		return nil, fmt.Errorf("create gateway certificate: %w", err)
	}
	serverKeyDER, err := x509.MarshalPKCS8PrivateKey(serverKey)
	if err != nil {
		return nil, err
	}
	serverCert, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: serverKeyDER}),
	)
	if err != nil {
		return nil, err
	}
	return &Authority{
		ca: ca, caKey: caKey, caPEM: caPEM, gatewayAddress: gatewayAddress,
		serverName: host, serverCert: serverCert,
	}, nil
}

func (a *Authority) ServerTLSConfig() *tls.Config {
	pool := x509.NewCertPool()
	pool.AddCert(a.ca)
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{a.serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
		NextProtos:   []string{"h2"},
	}
}

func (a *Authority) IssueNode(nodeID string) (CredentialBundle, error) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return CredentialBundle{}, err
	}
	certificatePEM, serial, notAfter, err := a.issue(nodeID, publicKey)
	if err != nil {
		return CredentialBundle{}, err
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return CredentialBundle{}, err
	}
	return CredentialBundle{
		NodeID: nodeID, GatewayAddress: a.gatewayAddress, ServerName: a.serverName,
		CACertificate: string(a.caPEM), Certificate: certificatePEM,
		PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})),
		Serial:     serial, NotAfter: notAfter,
	}, nil
}

func (a *Authority) SignCSR(nodeID, csrPEM string) (string, string, time.Time, error) {
	block, _ := pem.Decode([]byte(csrPEM))
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return "", "", time.Time{}, errors.New("invalid credential CSR")
	}
	request, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil || request.CheckSignature() != nil {
		return "", "", time.Time{}, errors.New("invalid credential CSR signature")
	}
	if request.Subject.CommonName != nodeID {
		return "", "", time.Time{}, errors.New("credential CSR identity does not match node")
	}
	if _, ok := request.PublicKey.(ed25519.PublicKey); !ok {
		return "", "", time.Time{}, errors.New("credential CSR must use an Ed25519 key")
	}
	return a.issue(nodeID, request.PublicKey)
}

func (a *Authority) issue(nodeID string, publicKey any) (string, string, time.Time, error) {
	serialBytes := make([]byte, 16)
	if _, err := rand.Read(serialBytes); err != nil {
		return "", "", time.Time{}, err
	}
	serialNumber := new(big.Int).SetBytes(serialBytes)
	notBefore := time.Now().UTC().Add(-5 * time.Minute)
	notAfter := notBefore.Add(nodeCertificateValidity)
	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject:      pkix.Name{CommonName: nodeID},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, a.ca, publicKey, a.caKey)
	if err != nil {
		return "", "", time.Time{}, err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), serialNumber.Text(16), notAfter, nil
}

func CertificateNodeID(certificate *x509.Certificate) string {
	if certificate == nil {
		return ""
	}
	return certificate.Subject.CommonName
}
