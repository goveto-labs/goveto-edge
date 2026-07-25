package certmanager

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/net/idna"
)

type Material struct {
	CertificatePEM string
	PrivateKeyPEM  string
	Fingerprint    string
	SerialNumber   string
	Domains        []string
	NotBefore      time.Time
	ExpiresAt      time.Time
	Issuer         string
	KeyAlgorithm   string
}

func ValidateMaterial(certificatePEM, privateKeyPEM string, now time.Time) (Material, error) {
	pair, err := tls.X509KeyPair([]byte(certificatePEM), []byte(privateKeyPEM))
	if err != nil {
		return Material{}, errors.New("certificate and private key do not match")
	}
	if len(pair.Certificate) == 0 {
		return Material{}, errors.New("certificate chain is empty")
	}
	certificates := make([]*x509.Certificate, 0, len(pair.Certificate))
	for _, raw := range pair.Certificate {
		certificate, parseErr := x509.ParseCertificate(raw)
		if parseErr != nil {
			return Material{}, fmt.Errorf("parse certificate chain: %w", parseErr)
		}
		certificates = append(certificates, certificate)
	}
	leaf := certificates[0]
	if now.Before(leaf.NotBefore) {
		return Material{}, fmt.Errorf("certificate is not valid before %s", leaf.NotBefore.UTC().Format(time.RFC3339))
	}
	if !now.Before(leaf.NotAfter) {
		return Material{}, fmt.Errorf("certificate expired at %s", leaf.NotAfter.UTC().Format(time.RFC3339))
	}
	if leaf.IsCA {
		return Material{}, errors.New("leaf certificate cannot be a CA")
	}
	if !allowsServerAuth(leaf.ExtKeyUsage) {
		return Material{}, errors.New("certificate is not valid for TLS server authentication")
	}
	if leaf.KeyUsage != 0 && leaf.KeyUsage&(x509.KeyUsageDigitalSignature|x509.KeyUsageKeyEncipherment) == 0 {
		return Material{}, errors.New("certificate key usage does not permit TLS")
	}
	if len(leaf.DNSNames) == 0 {
		return Material{}, errors.New("certificate must contain at least one DNS SAN")
	}

	intermediates := x509.NewCertPool()
	for _, certificate := range certificates[1:] {
		intermediates.AddCert(certificate)
	}
	roots, rootErr := x509.SystemCertPool()
	if rootErr != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	last := certificates[len(certificates)-1]
	if last.IsCA && last.CheckSignatureFrom(last) == nil {
		roots.AddCert(last)
	}
	if _, err = leaf.Verify(x509.VerifyOptions{
		Roots: roots, Intermediates: intermediates, CurrentTime: now,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		return Material{}, fmt.Errorf("certificate chain verification failed: %w", err)
	}

	domains := make([]string, 0, len(leaf.DNSNames))
	seen := map[string]struct{}{}
	for _, value := range leaf.DNSNames {
		domain, normalizeErr := NormalizeDomain(value, true)
		if normalizeErr != nil {
			return Material{}, fmt.Errorf("invalid DNS SAN %q: %w", value, normalizeErr)
		}
		if _, ok := seen[domain]; !ok {
			seen[domain] = struct{}{}
			domains = append(domains, domain)
		}
	}
	fingerprint := sha256.Sum256(leaf.Raw)
	return Material{
		CertificatePEM: certificatePEM, PrivateKeyPEM: privateKeyPEM,
		Fingerprint: hex.EncodeToString(fingerprint[:]), SerialNumber: leaf.SerialNumber.Text(16),
		Domains: domains, NotBefore: leaf.NotBefore, ExpiresAt: leaf.NotAfter,
		Issuer: leaf.Issuer.String(), KeyAlgorithm: keyAlgorithm(leaf.PublicKey),
	}, nil
}

func NormalizeDomains(values []string, wildcard bool) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		domain, err := NormalizeDomain(value, wildcard)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[domain]; ok {
			return nil, fmt.Errorf("duplicate domain %q", domain)
		}
		seen[domain] = struct{}{}
		result = append(result, domain)
	}
	if len(result) == 0 {
		return nil, errors.New("at least one domain is required")
	}
	return result, nil
}

func NormalizeDomain(value string, wildcard bool) (string, error) {
	value = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
	prefix := ""
	if strings.HasPrefix(value, "*.") {
		if !wildcard {
			return "", fmt.Errorf("wildcard domain is not allowed")
		}
		prefix, value = "*.", strings.TrimPrefix(value, "*.")
	}
	domain, err := idna.Lookup.ToASCII(value)
	if err != nil || domain == "" || strings.ContainsAny(domain, "/: ") || !strings.Contains(domain, ".") {
		return "", fmt.Errorf("invalid domain %q", value)
	}
	return prefix + domain, nil
}

func CoversDomains(certificateDomains, siteDomains []string) error {
	for _, domain := range siteDomains {
		covered := false
		for _, pattern := range certificateDomains {
			if matchesDomain(pattern, domain) {
				covered = true
				break
			}
		}
		if !covered {
			return fmt.Errorf("certificate SANs do not cover site domain %q", domain)
		}
	}
	return nil
}

func DecodeDomains(raw json.RawMessage) ([]string, error) {
	var domains []string
	if err := json.Unmarshal(raw, &domains); err != nil {
		return nil, fmt.Errorf("decode certificate domains: %w", err)
	}
	return domains, nil
}

func EncodeDomains(domains []string) json.RawMessage {
	data, _ := json.Marshal(domains)
	return data
}

func allowsServerAuth(usages []x509.ExtKeyUsage) bool {
	if len(usages) == 0 {
		return true
	}
	for _, usage := range usages {
		if usage == x509.ExtKeyUsageAny || usage == x509.ExtKeyUsageServerAuth {
			return true
		}
	}
	return false
}

func matchesDomain(pattern, domain string) bool {
	if pattern == domain {
		return true
	}
	if !strings.HasPrefix(pattern, "*.") {
		return false
	}
	suffix := strings.TrimPrefix(pattern, "*")
	return strings.HasSuffix(domain, suffix) && strings.Count(domain, ".") == strings.Count(pattern, ".")
}

func keyAlgorithm(publicKey any) string {
	switch key := publicKey.(type) {
	case *rsa.PublicKey:
		return fmt.Sprintf("RSA-%d", key.N.BitLen())
	case *ecdsa.PublicKey:
		return "ECDSA-" + key.Curve.Params().Name
	case ed25519.PublicKey:
		return "ED25519"
	default:
		return fmt.Sprintf("%T", publicKey)
	}
}

func ParsePEMCertificates(value string) ([]*x509.Certificate, error) {
	var result []*x509.Certificate
	data := []byte(value)
	for len(data) > 0 {
		block, rest := pem.Decode(data)
		if block == nil {
			break
		}
		data = rest
		if block.Type != "CERTIFICATE" {
			continue
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, err
		}
		result = append(result, certificate)
	}
	if len(result) == 0 {
		return nil, errors.New("no certificate PEM blocks found")
	}
	return result, nil
}

func InspectCertificatePEM(value string) (Material, error) {
	certificates, err := ParsePEMCertificates(value)
	if err != nil {
		return Material{}, err
	}
	leaf := certificates[0]
	domains := make([]string, 0, len(leaf.DNSNames))
	for _, value := range leaf.DNSNames {
		domain, normalizeErr := NormalizeDomain(value, true)
		if normalizeErr != nil {
			return Material{}, normalizeErr
		}
		domains = append(domains, domain)
	}
	fingerprint := sha256.Sum256(leaf.Raw)
	return Material{CertificatePEM: value, Fingerprint: hex.EncodeToString(fingerprint[:]), SerialNumber: leaf.SerialNumber.Text(16), Domains: domains, NotBefore: leaf.NotBefore, ExpiresAt: leaf.NotAfter, Issuer: leaf.Issuer.String(), KeyAlgorithm: keyAlgorithm(leaf.PublicKey)}, nil
}
