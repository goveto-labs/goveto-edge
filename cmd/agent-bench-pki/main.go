package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"goveto-edge/internal/edgeagent"
	"goveto-edge/internal/edgeprotocol"
	cachepolicy "goveto-edge/internal/policy"
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
	domains := []string{"benchmark.example.test", "cache.benchmark.example.test", "cache-alt.benchmark.example.test", "cache-rules.benchmark.example.test", "multi.benchmark.example.test", "resilient.benchmark.example.test", "limit.benchmark.example.test"}
	edgeCert, edgeKey := issueLeaf(caCert, caKey, domains[0], domains, nil, true)
	write(filepath.Join(*output, "certs", "ca.crt"), caPEM, 0600)
	write(filepath.Join(*output, "certs", "server.crt"), serverCert, 0600)
	write(filepath.Join(*output, "certs", "server.key"), serverKey, 0600)
	identity := edgeagent.Identity{NodeID: *nodeID, GatewayAddress: *gatewayAddress, ServerName: *serverName, CACertificate: string(caPEM), Certificate: string(clientCert), PrivateKey: string(clientKey)}
	if err := edgeagent.WriteIdentity(filepath.Join(*output, "identity.json"), identity); err != nil {
		log.Fatal(err)
	}
	listener := edgeprotocol.ListenerConfig{HTTPEnabled: true, HTTPPort: 8080, HTTPSEnabled: true, HTTPSPort: 8444, HTTP2Enabled: true, HTTP3Enabled: true, TLSMinVersion: "TLS1_3"}
	certificate := edgeprotocol.CertificateConfig{CertificatePEM: string(edgeCert), PrivateKeyPEM: string(edgeKey)}
	originPolicy := edgeprotocol.DefaultOriginPolicy()
	originPolicy.Transport.KeepAliveIdleTimeoutMS = 15000
	originPolicy.PassiveHealth.Enabled = false
	originPolicy.Retry = edgeprotocol.OriginRetryConfig{}
	cache, complexCache, err := benchmarkCachePolicies()
	if err != nil {
		log.Fatal(err)
	}
	rateLimit := cachepolicy.DefaultRateLimitPolicy()
	rateLimit.Enabled = true
	rateLimit.Rules = []cachepolicy.RateLimitRule{{ID: "benchmark-global", Name: "benchmark global limiter", Enabled: true, Key: "GLOBAL", Requests: 100, WindowSeconds: 60, Burst: 0, BanSeconds: 300, StatusCode: 429}}
	resilientPolicy := edgeprotocol.DefaultOriginPolicy()
	resilientPolicy.Transport.KeepAliveIdleTimeoutMS = 15000

	sites := []edgeprotocol.SiteConfig{
		{SiteID: "benchmark-site", Version: 1, Domains: []string{domains[0]}, Listener: listener, Certificates: []edgeprotocol.CertificateConfig{certificate}, Origins: []edgeprotocol.OriginConfig{{Protocol: "http", Address: "origin:8080"}}, OriginPolicy: originPolicy},
		{SiteID: "benchmark-cache", Version: 1, Domains: domains[1:3], Listener: listener, Certificates: []edgeprotocol.CertificateConfig{certificate}, Origins: []edgeprotocol.OriginConfig{{Protocol: "http", Address: "origin:8080"}}, OriginPolicy: originPolicy, Cache: asMap(cache)},
		{SiteID: "benchmark-cache-rules", Version: 1, Domains: []string{domains[3]}, Listener: listener, Certificates: []edgeprotocol.CertificateConfig{certificate}, Origins: []edgeprotocol.OriginConfig{{Protocol: "http", Address: "origin:8080"}}, OriginPolicy: originPolicy, Cache: asMap(complexCache)},
		{SiteID: "benchmark-multi", Version: 1, Domains: []string{domains[4]}, Listener: listener, Certificates: []edgeprotocol.CertificateConfig{certificate}, Origins: []edgeprotocol.OriginConfig{{Protocol: "http", Address: "origin:8080"}, {Protocol: "http", Address: "origin2:8080"}}, Scheduler: "round_robin", OriginPolicy: originPolicy},
		{SiteID: "benchmark-resilient", Version: 1, Domains: []string{domains[5]}, Listener: listener, Certificates: []edgeprotocol.CertificateConfig{certificate}, Origins: []edgeprotocol.OriginConfig{{Protocol: "http", Address: "origin:8080"}, {Protocol: "http", Address: "origin2:8080"}}, Scheduler: "first", OriginPolicy: resilientPolicy},
		{SiteID: "benchmark-limit", Version: 1, Domains: []string{domains[6]}, Listener: listener, Certificates: []edgeprotocol.CertificateConfig{certificate}, Origins: []edgeprotocol.OriginConfig{{Protocol: "http", Address: "origin:8080"}}, OriginPolicy: originPolicy, RateLimit: asMap(rateLimit)},
	}
	tasks := []edgeprotocol.AgentTask{task(edgeprotocol.TaskNodeCacheConfig, edgeprotocol.NodeCacheConfig{CacheDirectory: "/opt/goveto-edge/cache", AutoMaxSize: false, MaxSizeBytes: 24 << 20, MaxDiskUsagePercent: 90})}
	for _, site := range sites {
		tasks = append(tasks, task(edgeprotocol.TaskApplySiteConfig, site))
	}
	encodedTasks, err := json.MarshalIndent(tasks, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	write(filepath.Join(*output, "initial-tasks.json"), encodedTasks, 0600)
}

func task(kind string, value any) edgeprotocol.AgentTask {
	payload, err := json.Marshal(value)
	if err != nil {
		log.Fatal(err)
	}
	return edgeprotocol.AgentTask{ID: uuid.NewString(), Kind: kind, Payload: payload}
}

func asMap(value any) map[string]any {
	data, err := json.Marshal(value)
	if err != nil {
		log.Fatal(err)
	}
	var result map[string]any
	if err = json.Unmarshal(data, &result); err != nil {
		log.Fatal(err)
	}
	return result
}

func benchmarkCachePolicies() (cachepolicy.CachePolicy, cachepolicy.CachePolicy, error) {
	standard := cachepolicy.DefaultCachePolicy()
	standard.AllowPurgeMethod = true
	standard.Stale.IfErrorSeconds = 3600
	standard.Stale.WhileRevalidateSeconds = 30
	standard.Rules = []cachepolicy.CacheRule{benchmarkCacheRule("Default", cachepolicy.CacheConditions{
		GroupOperator: "OR",
		Groups: []cachepolicy.CacheConditionGroup{{
			Operator: "OR",
			Rules:    []cachepolicy.CacheConditionRule{{Type: "ALL"}},
		}},
	})}
	if err := standard.NormalizeAndValidate(); err != nil {
		return cachepolicy.CachePolicy{}, cachepolicy.CachePolicy{}, fmt.Errorf("standard benchmark cache policy: %w", err)
	}

	complex := standard
	complex.CacheKey.Parts = []string{
		cachepolicy.CacheKeyPartMethod,
		cachepolicy.CacheKeyPartScheme,
		cachepolicy.CacheKeyPartHost,
		cachepolicy.CacheKeyPartPath,
		cachepolicy.CacheKeyPartQuery,
	}
	complex.CacheKey.Headers = []string{"X-Cache-Variant", "Accept-Language"}
	complex.CacheKey.Hash = true
	complex.CacheKey.Hide = true
	complex.Rules = []cachepolicy.CacheRule{benchmarkCacheRule("Early extension", cachepolicy.CacheConditions{
		GroupOperator: "OR",
		Groups: []cachepolicy.CacheConditionGroup{{
			Operator: "OR",
			Rules:    []cachepolicy.CacheConditionRule{{Type: "EXTENSION", Values: []string{"css"}}},
		}},
	})}
	for index := 1; index < 30; index++ {
		complex.Rules = append(complex.Rules, benchmarkCacheRule(fmt.Sprintf("Decoy %02d", index), cachepolicy.CacheConditions{
			GroupOperator: "AND",
			Groups: []cachepolicy.CacheConditionGroup{
				{
					Operator: "OR",
					Rules: []cachepolicy.CacheConditionRule{
						{Type: "EXTENSION", Values: []string{fmt.Sprintf("cachebench%d", index)}},
						{Type: "PATH_PREFIX", Values: []string{fmt.Sprintf("/unmatched/%d/", index)}},
					},
				},
				{
					Operator: "AND",
					Rules: []cachepolicy.CacheConditionRule{{
						Type: "PATH_REGEX", Value: fmt.Sprintf(`^/unmatched/%d/[0-9]+$`, index),
					}},
				},
			},
		}))
	}
	complex.Rules = append(complex.Rules,
		benchmarkCacheRule("Late grouped match", cachepolicy.CacheConditions{
			GroupOperator: "AND",
			Groups: []cachepolicy.CacheConditionGroup{
				{
					Operator: "OR",
					Rules: []cachepolicy.CacheConditionRule{
						{Type: "EXTENSION", Values: []string{"bin"}},
						{Type: "PATH_PREFIX", Values: []string{"/cache-rules/late/"}},
					},
				},
				{
					Operator: "AND",
					Rules: []cachepolicy.CacheConditionRule{{
						Type: "PATH_REGEX", Value: `^/cache-rules/late/[0-9]+[.]bin$`,
					}},
				},
			},
		}),
		benchmarkCacheRule("Fallback", cachepolicy.CacheConditions{
			GroupOperator: "OR",
			Groups: []cachepolicy.CacheConditionGroup{{
				Operator: "OR",
				Rules:    []cachepolicy.CacheConditionRule{{Type: "ALL"}},
			}},
		}),
	)
	if err := complex.NormalizeAndValidate(); err != nil {
		return cachepolicy.CachePolicy{}, cachepolicy.CachePolicy{}, fmt.Errorf("complex benchmark cache policy: %w", err)
	}
	return standard, complex, nil
}

func benchmarkCacheRule(name string, conditions cachepolicy.CacheConditions) cachepolicy.CacheRule {
	return cachepolicy.CacheRule{
		Name: name,
		TTL: cachepolicy.CacheTTL{
			DefaultSeconds: 3600,
			Status:         map[string]int{"200": 3600, "206": 3600, "301": 3600, "404": 60},
			ClientSeconds:  300,
		},
		Conditions: conditions,
	}
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
