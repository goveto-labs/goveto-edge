package edgeagent

import (
	"bytes"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

type Identity struct {
	NodeID         string `json:"node_id"`
	GatewayAddress string `json:"gateway_address"`
	ServerName     string `json:"server_name"`
	CACertificate  string `json:"ca_certificate_pem"`
	Certificate    string `json:"certificate_pem"`
	PrivateKey     string `json:"private_key_pem"`
	PendingCSR     string `json:"pending_csr_pem,omitempty"`
	PendingKey     string `json:"pending_private_key_pem,omitempty"`
}

func LoadIdentity(path string) (Identity, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Identity{}, fmt.Errorf("read agent identity: %w", err)
	}
	var identity Identity
	if err := json.Unmarshal(data, &identity); err != nil {
		return Identity{}, fmt.Errorf("decode agent identity: %w", err)
	}
	if err := identity.Validate(); err != nil {
		return Identity{}, err
	}
	return identity, nil
}

func (i Identity) Validate() error {
	if i.NodeID == "" || i.GatewayAddress == "" || i.ServerName == "" ||
		i.CACertificate == "" || i.Certificate == "" || i.PrivateKey == "" {
		return errors.New("agent identity is incomplete")
	}
	if _, err := uuid.Parse(i.NodeID); err != nil {
		return errors.New("agent node_id must be a UUID")
	}
	pair, err := tls.X509KeyPair([]byte(i.Certificate), []byte(i.PrivateKey))
	if err != nil {
		return fmt.Errorf("load agent certificate: %w", err)
	}
	certificate, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return fmt.Errorf("parse agent certificate: %w", err)
	}
	if certificate.Subject.CommonName != i.NodeID {
		return errors.New("agent certificate identity does not match node_id")
	}
	if block, _ := pem.Decode([]byte(i.CACertificate)); block == nil || block.Type != "CERTIFICATE" {
		return errors.New("agent CA certificate is invalid")
	}
	if (i.PendingCSR == "") != (i.PendingKey == "") {
		return errors.New("pending agent credential is incomplete")
	}
	if i.PendingCSR != "" {
		csrBlock, _ := pem.Decode([]byte(i.PendingCSR))
		keyBlock, _ := pem.Decode([]byte(i.PendingKey))
		if csrBlock == nil || csrBlock.Type != "CERTIFICATE REQUEST" || keyBlock == nil || keyBlock.Type != "PRIVATE KEY" {
			return errors.New("pending agent credential is invalid")
		}
		request, err := x509.ParseCertificateRequest(csrBlock.Bytes)
		if err != nil || request.CheckSignature() != nil || request.Subject.CommonName != i.NodeID {
			return errors.New("pending agent credential request is invalid")
		}
		parsedKey, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
		if err != nil {
			return errors.New("pending agent private key is invalid")
		}
		privateKey, privateOK := parsedKey.(ed25519.PrivateKey)
		publicKey, publicOK := request.PublicKey.(ed25519.PublicKey)
		if !privateOK || !publicOK || !bytes.Equal(privateKey.Public().(ed25519.PublicKey), publicKey) {
			return errors.New("pending agent credential key does not match request")
		}
	}
	return nil
}

func WriteIdentity(path string, identity Identity) error {
	if err := identity.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(identity)
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}
