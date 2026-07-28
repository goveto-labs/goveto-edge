package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"flag"
	"log"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"goveto-edge/internal/edgeagent"
	"goveto-edge/internal/edgeprotocol"
)

func main() {
	output := flag.String("output", "deploy/benchmark/state", "output directory")
	gatewayAddress := flag.String("gateway-address", "gateway:8443", "address written to agent identity")
	serverName := flag.String("server-name", "gateway", "gateway TLS server name")
	nodeID := flag.String("node-id", uuid.NewString(), "agent node UUID")
	flag.Parse()
	if _, err := uuid.Parse(*nodeID); err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(*output, "certs"), 0700); err != nil {
		log.Fatal(err)
	}
	caCert, caKey, caPEM, _ := issueCA()
	serverCert, serverKey := issueLeaf(caCert, caKey, "benchmark-gateway", []string{*serverName}, nil, true)
	clientCert, clientKey := issueLeaf(caCert, caKey, *nodeID, nil, nil, false)
	edgeCert, edgeKey := issueLeaf(caCert, caKey, "benchmark.example.test", []string{"benchmark.example.test"}, nil, true)
	write(filepath.Join(*output, "certs", "ca.crt"), caPEM, 0600)
	write(filepath.Join(*output, "certs", "server.crt"), serverCert, 0600)
	write(filepath.Join(*output, "certs", "server.key"), serverKey, 0600)
	identity := edgeagent.Identity{NodeID: *nodeID, GatewayAddress: *gatewayAddress, ServerName: *serverName, CACertificate: string(caPEM), Certificate: string(clientCert), PrivateKey: string(clientKey)}
	if err := edgeagent.WriteIdentity(filepath.Join(*output, "identity.json"), identity); err != nil {
		log.Fatal(err)
	}
	site := edgeprotocol.SiteConfig{SiteID: "benchmark-site", Version: 1, Domains: []string{"benchmark.example.test"}, Listener: edgeprotocol.ListenerConfig{HTTPEnabled: true, HTTPPort: 8080, HTTPSEnabled: true, HTTPSPort: 8444, HTTP2Enabled: true, HTTP3Enabled: true, TLSMinVersion: "TLS1_3"}, Certificates: []edgeprotocol.CertificateConfig{{CertificatePEM: string(edgeCert), PrivateKeyPEM: string(edgeKey)}}, Origins: []edgeprotocol.OriginConfig{{Protocol: "http", Address: "origin:8080"}}}
	payload, err := json.Marshal(site)
	if err != nil {
		log.Fatal(err)
	}
	task, err := json.MarshalIndent(edgeprotocol.AgentTask{ID: uuid.NewString(), Kind: edgeprotocol.TaskApplySiteConfig, Payload: payload}, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	write(filepath.Join(*output, "initial-task.json"), task, 0600)
}

func issueCA() (*x509.Certificate, ed25519.PrivateKey, []byte, []byte) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: serial(), Subject: pkix.Name{CommonName: "Edge Agent Benchmark CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(1, 0, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	if err != nil {
		log.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		log.Fatal(err)
	}
	key, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		log.Fatal(err)
	}
	return certificate, private, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key})
}

func issueLeaf(ca *x509.Certificate, caKey ed25519.PrivateKey, commonName string, dnsNames []string, ips []net.IP, server bool) ([]byte, []byte) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	usage := x509.ExtKeyUsageClientAuth
	if server {
		usage = x509.ExtKeyUsageServerAuth
	}
	template := &x509.Certificate{SerialNumber: serial(), Subject: pkix.Name{CommonName: commonName}, DNSNames: dnsNames, IPAddresses: ips, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(1, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, public, caKey)
	if err != nil {
		log.Fatal(err)
	}
	key, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		log.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key})
}

func serial() *big.Int {
	value, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		log.Fatal(err)
	}
	return value
}
func write(path string, data []byte, mode os.FileMode) {
	if err := os.WriteFile(path, data, mode); err != nil {
		log.Fatal(err)
	}
}
