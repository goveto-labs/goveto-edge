package e2e

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"goveto-edge/internal/edgeprotocol"
	securitypolicy "goveto-edge/internal/policy"
)

// TestCacheLifecycleE2E is the minimum viable data-plane link:
//
//	origin → apply site config → MISS → HIT → purge → MISS
//
// It exercises the production gRPC task path end to end: the real agent
// receives an APPLY_SITE_CONFIG task over mTLS, applies it to its embedded
// Caddy instance, and serves real HTTP traffic. No internal edgeagent methods
// are called — the only handle is the network.
func TestCacheLifecycleE2E(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()

	var originHits atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := originHits.Add(1)
		w.Header().Set("Cache-Control", "public, max-age=120")
		fmt.Fprintf(w, "origin-body-%d", count)
	}))
	defer origin.Close()

	config := edgeprotocol.SiteConfig{
		SiteID: "lifecycle-site", Version: 1, Domains: []string{"lifecycle.example.test"},
		Listener: edgeprotocol.ListenerConfig{HTTPEnabled: true, HTTPPort: h.httpPort},
		Origins:  []edgeprotocol.OriginConfig{{Protocol: "http", Address: stripScheme(origin.URL), Weight: 1}},
		Cache:    policyToMap(t, catchAllCachePolicy(120)),
	}
	h.applySite(ctx, config)

	// First request: cold MISS against the origin.
	first := h.request(ctx, config.Domains[0], http.MethodGet, "/asset.css", nil)
	if first.status != http.StatusOK || first.header.Get("X-Cache") != "MISS" || originHits.Load() != 1 {
		t.Fatalf("cold request: status=%d x-cache=%q origins=%d", first.status, first.header.Get("X-Cache"), originHits.Load())
	}

	// Second request: served from cache without touching the origin.
	second := h.request(ctx, config.Domains[0], http.MethodGet, "/asset.css", nil)
	if second.header.Get("X-Cache") != "HIT" || second.body != first.body || originHits.Load() != 1 {
		t.Fatalf("cached request: x-cache=%q body=%q origins=%d", second.header.Get("X-Cache"), second.body, originHits.Load())
	}

	// Purge the URL over the management protocol and confirm the next request
	// is a fresh MISS that re-reaches the origin.
	result := h.purge(ctx, edgeprotocol.PurgeRequest{SiteID: config.SiteID, Type: "URL", Values: []string{"/asset.css"}})
	if result.Objects != 1 {
		t.Fatalf("purge objects=%d, want 1", result.Objects)
	}
	afterPurge := h.request(ctx, config.Domains[0], http.MethodGet, "/asset.css", nil)
	if afterPurge.header.Get("X-Cache") != "MISS" || originHits.Load() != 2 || afterPurge.body == second.body {
		t.Fatalf("post-purge MISS: x-cache=%q origins=%d body=%q", afterPurge.header.Get("X-Cache"), originHits.Load(), afterPurge.body)
	}
}

// TestMultiOriginFailoverE2E verifies weighted origin pools with passive health
// checks: a failing primary is ejected and traffic fails over to the backup,
// then recovers once the primary is healthy again.
func TestMultiOriginFailoverE2E(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()

	var primaryHealthy atomic.Bool
	primaryHealthy.Store(true)
	var primaryHits atomic.Int32
	var backupHits atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		primaryHits.Add(1)
		if !primaryHealthy.Load() {
			http.Error(w, "primary down", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, "primary")
	}))
	defer primary.Close()
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backupHits.Add(1)
		fmt.Fprint(w, "backup")
	}))
	defer backup.Close()

	config := edgeprotocol.SiteConfig{
		SiteID: "failover-site", Version: 1, Domains: []string{"failover.example.test"},
		Listener: edgeprotocol.ListenerConfig{HTTPEnabled: true, HTTPPort: h.httpPort},
		Origins: []edgeprotocol.OriginConfig{
			{Protocol: "http", Address: stripScheme(primary.URL), Weight: 1},
			{Protocol: "http", Address: stripScheme(backup.URL), Weight: 1, Priority: 10},
		},
		OriginPolicy: edgeprotocol.OriginPolicyConfig{
			TimeoutMS: 2000,
			PassiveHealth: edgeprotocol.OriginPassiveHealthConfig{
				Enabled: true, FailDurationMS: 400, MaxFails: 1, UnhealthyStatus: []int{502, 503, 504},
			},
			Retry: edgeprotocol.OriginRetryConfig{Retries: 1, TryDurationMS: 1000, TryIntervalMS: 1},
		},
	}
	// No Cache field: every request exercises live origin selection so failover
	// is observable without cache hits masking the result.
	h.applySite(ctx, config)

	// Healthy primary serves first.
	if body, _ := getEdge(t, h, config.Domains[0]); body != "primary" {
		t.Fatalf("expected primary, got %q", body)
	}

	// Primary starts failing; requests must fail over to backup.
	primaryHealthy.Store(false)
	if body, status := getEdge(t, h, config.Domains[0]); body != "backup" || status != http.StatusOK {
		t.Fatalf("expected failover to backup, got body=%q status=%d", body, status)
	}

	// Primary recovers and, after the ejection window, is retried.
	primaryHealthy.Store(true)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if body, _ := getEdge(t, h, config.Domains[0]); body == "primary" {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(80 * time.Millisecond):
		}
	}
	t.Fatalf("primary never recovered: primaryHits=%d backupHits=%d", primaryHits.Load(), backupHits.Load())
}

// TestRangeLargeFileE2E confirms byte-range requests against a large object are
// served correctly and the full representation is cached so a second range
// request does not reach the origin.
func TestRangeLargeFileE2E(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()

	video := strings.Repeat("0123456789abcdef", 16<<10) // 256 KiB
	var originHits atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originHits.Add(1)
		w.Header().Set("Cache-Control", "public, max-age=120")
		w.Header().Set("Accept-Ranges", "bytes")
		http.ServeContent(w, r, "video.bin", time.Unix(1, 0), strings.NewReader(video))
	}))
	defer origin.Close()

	cache := catchAllCachePolicy(120)
	cache.CacheRangeRequests = true
	config := edgeprotocol.SiteConfig{
		SiteID: "range-site", Version: 1, Domains: []string{"range.example.test"},
		Listener: edgeprotocol.ListenerConfig{HTTPEnabled: true, HTTPPort: h.httpPort},
		Origins:  []edgeprotocol.OriginConfig{{Protocol: "http", Address: stripScheme(origin.URL), Weight: 1}},
		Cache:    policyToMap(t, cache),
	}
	h.applySite(ctx, config)

	headers := http.Header{"Range": {"bytes=100-199"}}
	first := h.request(ctx, config.Domains[0], http.MethodGet, "/video.bin", headers)
	if first.status != http.StatusPartialContent || first.body != video[100:200] {
		t.Fatalf("first range: status=%d bodyLen=%d", first.status, len(first.body))
	}
	if first.header.Get("Content-Range") != "bytes 100-199/262144" || originHits.Load() != 1 {
		t.Fatalf("range metadata: content-range=%q origins=%d", first.header.Get("Content-Range"), originHits.Load())
	}

	// A different byte range reuses the cached full object — no origin hit.
	other := h.request(ctx, config.Domains[0], http.MethodGet, "/video.bin", http.Header{"Range": {"bytes=300-399"}})
	if other.status != http.StatusPartialContent || other.body != video[300:400] || originHits.Load() != 1 {
		t.Fatalf("second range not served from cache: status=%d origins=%d", other.status, originHits.Load())
	}
}

// TestControlPlaneRestartResumesJobs simulates a control-plane restart: the
// gateway is torn down mid-session, brought back up, and the agent must
// reconnect and accept a fresh publish. This is the data-plane half of "control
// plane restart Job recovery" — the agent's reconnect loop is what lets a
// restarted control plane re-deliver pending work.
func TestControlPlaneRestartResumesJobs(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()

	var originHits atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count := originHits.Add(1)
		fmt.Fprintf(w, "resumed-%d", count)
	}))
	defer origin.Close()

	config := edgeprotocol.SiteConfig{
		SiteID: "resume-site", Version: 1, Domains: []string{"resume.example.test"},
		Listener: edgeprotocol.ListenerConfig{HTTPEnabled: true, HTTPPort: h.httpPort},
		Origins:  []edgeprotocol.OriginConfig{{Protocol: "http", Address: stripScheme(origin.URL), Weight: 1}},
	}
	h.applySite(ctx, config)

	// Baseline: the site is live.
	if body := mustGet(t, h, config.Domains[0]); !strings.HasPrefix(body, "resumed-") {
		t.Fatalf("site not live before restart: %q", body)
	}

	// Simulate the control plane going away: stop the gRPC server forcefully
	// (a graceful stop would block on the agent's long-lived stream). The
	// agent's stream errors out and it enters its reconnect backoff.
	h.restartGateway(ctx)

	// Bring the control plane back: a fresh gateway on the same address. The
	// agent (still running) reconnects to it.
	restartCtx, restartCancel := context.WithTimeout(ctx, 5*time.Second)
	defer restartCancel()
	h.restartGateway(restartCtx)
	h.waitForAgentReconnect(ctx)

	// Re-publish a new version after the restart; it must take effect.
	config.Version = 2
	h.applySite(ctx, config)

	if body := mustGet(t, h, config.Domains[0]); !strings.HasPrefix(body, "resumed-") {
		t.Fatalf("site not live after restart+republish: %q", body)
	}
}

// TestMultiDomainHTTPSServesAllSANs confirms a site with multiple domains
// terminates TLS for each configured SAN.
func TestMultiDomainHTTPSServesAllSANs(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "secure")
	}))
	defer origin.Close()

	domains := []string{"alpha.example.test", "beta.example.test", "gamma.example.test"}
	httpsPort := freePort(t)
	certPEM, keyPEM := selfSignedCert(t, domains...)
	config := edgeprotocol.SiteConfig{
		SiteID: "multi-https-site", Version: 1, Domains: domains,
		Listener: edgeprotocol.ListenerConfig{
			HTTPSEnabled: true, HTTPSPort: httpsPort, HTTP2Enabled: true, TLSMinVersion: "TLS1_2",
		},
		Certificates: []edgeprotocol.CertificateConfig{{CertificatePEM: certPEM, PrivateKeyPEM: keyPEM}},
		Origins:      []edgeprotocol.OriginConfig{{Protocol: "http", Address: stripScheme(origin.URL), Weight: 1}},
	}
	h.applySite(ctx, config)

	client := &http.Client{Timeout: 5 * time.Second}
	for _, domain := range domains {
		// The URL host is 127.0.0.1, so override TLS ServerName to the site
		// domain; that is the SNI Caddy matches against its certificate policy.
		transport := &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true, ServerName: domain, MinVersion: tls.VersionTLS12},
		}
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet,
			fmt.Sprintf("https://127.0.0.1:%d/", httpsPort), nil)
		request.Host = domain
		client.Transport = transport
		response, err := client.Do(request)
		transport.CloseIdleConnections()
		if err != nil {
			t.Fatalf("TLS request for %s: %v", domain, err)
		}
		body := readAll(t, response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusOK || body != "secure" {
			t.Fatalf("domain %s: status=%d body=%q", domain, response.StatusCode, body)
		}
	}
}

// TestWAFBlocksMaliciousRequestE2E confirms the WAF evaluates before the
// origin: a custom rule group hard-blocks matching requests with 403 and never
// reaches the upstream, while benign requests pass through.
func TestWAFBlocksMaliciousRequestE2E(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()

	var originHits atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		originHits.Add(1)
		fmt.Fprint(w, "allowed")
	}))
	defer origin.Close()

	waf := securitypolicy.DefaultWAFPolicy()
	waf.Enabled = true
	waf.RuleSets = []securitypolicy.WAFRuleSet{{
		ID: "custom", Name: "Custom", Enabled: true,
		Rules: []securitypolicy.WAFRule{{
			ID: "block-suspicious-header", Name: "Suspicious header", Enabled: true, Type: securitypolicy.WAFRuleTypeMatch,
			Conditions: securitypolicy.WAFConditions{Operator: "AND", Groups: []securitypolicy.WAFConditionGroup{{Operator: "AND", Conditions: []securitypolicy.WAFCondition{{Field: "HEADER", FieldName: "X-Malicious", Operator: "EQUALS", Value: "yes"}}}}},
			Action:     securitypolicy.WAFAction{Type: securitypolicy.WAFActionBlock, StatusCode: http.StatusForbidden},
		}},
	}}
	config := edgeprotocol.SiteConfig{
		SiteID: "waf-site", Version: 1, Domains: []string{"waf.example.test"},
		Listener: edgeprotocol.ListenerConfig{HTTPEnabled: true, HTTPPort: h.httpPort},
		Origins:  []edgeprotocol.OriginConfig{{Protocol: "http", Address: stripScheme(origin.URL), Weight: 1}},
		WAF:      policyToMap(t, waf),
	}
	h.applySite(ctx, config)

	// Benign request reaches the origin.
	benign := h.request(ctx, config.Domains[0], http.MethodGet, "/", nil)
	if benign.status != http.StatusOK || benign.body != "allowed" || originHits.Load() != 1 {
		t.Fatalf("benign request blocked: status=%d body=%q origins=%d", benign.status, benign.body, originHits.Load())
	}

	// Malicious request is blocked at the edge and never reaches the origin.
	malicious := h.request(ctx, config.Domains[0], http.MethodGet, "/", http.Header{"X-Malicious": {"yes"}})
	if malicious.status != http.StatusForbidden {
		t.Fatalf("malicious request not blocked: status=%d", malicious.status)
	}
	if rule := malicious.header.Get("X-Goveto-WAF-Rule"); rule != "block-suspicious-header" {
		t.Fatalf("WAF rule header=%q, want block-suspicious-header", rule)
	}
	if originHits.Load() != 1 {
		t.Fatalf("blocked request reached the origin: hits=%d", originHits.Load())
	}
}

// --- helpers ----------------------------------------------------------------

func stripScheme(url string) string {
	return strings.TrimPrefix(strings.TrimPrefix(url, "http://"), "https://")
}

func getEdge(t *testing.T, h *harness, host string) (string, int) {
	t.Helper()
	response := h.request(t.Context(), host, http.MethodGet, "/", nil)
	return response.body, response.status
}

func mustGet(t *testing.T, h *harness, host string) string {
	t.Helper()
	body, status := getEdge(t, h, host)
	if status != http.StatusOK {
		t.Fatalf("GET %s status=%d body=%q", host, status, body)
	}
	return body
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func readAll(t *testing.T, r io.Reader) string {
	t.Helper()
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// selfSignedCert generates an ed25519 leaf certificate valid for the given SANs.
func selfSignedCert(t *testing.T, domains ...string) (string, string) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: randomSerial(t), Subject: pkix.Name{CommonName: domains[0]},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		DNSNames:    domains,
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	return certPEM, keyPEM
}

func randomSerial(t *testing.T) *big.Int {
	t.Helper()
	return big.NewInt(time.Now().UnixNano())
}

// restartGateway tears down the serving gRPC server and brings up a fresh one
// on the same address, modelling a control-plane process restart. It uses
// Stop() (not GracefulStop) because the agent's management stream is
// long-lived and a graceful stop would wait for it forever.
func (h *harness) restartGateway(ctx context.Context) {
	h.grpcServer.Stop()
	// The listener is released asynchronously after Stop(); retry briefly so
	// the restart does not race the port teardown.
	var listener net.Listener
	var err error
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		listener, err = net.Listen("tcp", h.gatewayAddr)
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			h.t.Fatalf("rebind gateway listener: %v", err)
		case <-time.After(50 * time.Millisecond):
		}
	}
	if listener == nil {
		h.t.Fatalf("rebind gateway listener: %v", err)
	}
	h.listener = listener
	h.grpcServer = grpc.NewServer(
		grpc.Creds(credentials.NewTLS(h.authority.ServerTLSConfig())),
		grpc.ForceServerCodec(edgeprotocol.JSONCodec{}),
	)
	restarted := h.grpcServer
	edgeprotocol.RegisterManagementServer(restarted, h.gateway)
	go func() { _ = restarted.Serve(listener) }()
	h.t.Cleanup(restarted.Stop)
}
