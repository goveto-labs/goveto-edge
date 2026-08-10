package edgecontrol

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
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
	return newAuthority(cipher.Derive("goveto-edge/agent-mtls/ca/v1")[:ed25519.SeedSize], gatewayAddress)
}

// NewAuthorityWithCAKey builds the Agent PKI from an independently managed CA
// seed. Existing installations should initialize this key with the legacy
// derived seed before rotating the shared credential master key.
func NewAuthorityWithCAKey(encodedKey, gatewayAddress string) (*Authority, error) {
	seed, err := base64.StdEncoding.DecodeString(encodedKey)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("agent CA master key must be base64-encoded 32 bytes")
	}
	return newAuthority(seed, gatewayAddress)
}

func newAuthority(caSeed []byte, gatewayAddress string) (*Authority, error) {
	host, _, err := net.SplitHostPort(gatewayAddress)
	if err != nil || strings.TrimSpace(host) == "" {
		return nil, fmt.Errorf("agent gateway public address must be host:port: %w", err)
	}

	caKey := ed25519.NewKeyFromSeed(caSeed)
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

	serverSeed := sha256.Sum256(append(append([]byte(nil), caSeed...), []byte("goveto-edge/agent-mtls/server/v2\x00"+host)...))
	serverKey := ed25519.NewKeyFromSeed(serverSeed[:])
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

// CAFingerprint returns a short, stable identifier for the agent CA certificate
// so callers can detect that the CA identity changed across restarts.
func (a *Authority) CAFingerprint() string {
	sum := sha256.Sum256(a.ca.Raw)
	return hex.EncodeToString(sum[:8])
}

// PinAgentCA records the agent CA fingerprint in dataDir and fails when the CA
// identity changed since the last successful boot. Rotating the agent CA
// invalidates every issued agent mTLS credential, so when the CA key is not
// explicitly pinned via AGENT_CA_MASTER_KEY (i.e. it is derived from the shared
// credential master key) a changed fingerprint is treated as an accidental
// rotation and the process refuses to start. When pinned is true a deliberate
// rotation is allowed and the recorded fingerprint is updated after warning
// that existing agent credentials must be re-issued.
func PinAgentCA(dataDir string, authority *Authority, pinned bool) error {
	secretsDir := filepath.Join(dataDir, "secrets")
	if err := os.MkdirAll(secretsDir, 0700); err != nil {
		return fmt.Errorf("create secrets directory: %w", err)
	}
	path := filepath.Join(secretsDir, "agent-ca-fingerprint")
	current := authority.CAFingerprint()
	stored, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read agent CA fingerprint: %w", err)
	}
	if len(strings.TrimSpace(string(stored))) == 0 {
		if err = os.WriteFile(path, []byte(current+"\n"), 0600); err != nil {
			return fmt.Errorf("record agent CA fingerprint: %w", err)
		}
		return nil
	}
	if strings.TrimSpace(string(stored)) == current {
		return nil
	}
	if !pinned {
		return errors.New("agent certificate authority identity changed because the credential master key changed; pin the CA with AGENT_CA_MASTER_KEY or restore the previous master key to avoid invalidating every issued agent credential")
	}
	slog.Warn("agent certificate authority rotated via AGENT_CA_MASTER_KEY; existing agent credentials must be re-issued")
	if err = os.WriteFile(path, []byte(current+"\n"), 0600); err != nil {
		return fmt.Errorf("update agent CA fingerprint: %w", err)
	}
	return nil
}

func CertificateNodeID(certificate *x509.Certificate) string {
	if certificate == nil {
		return ""
	}
	return certificate.Subject.CommonName
}
