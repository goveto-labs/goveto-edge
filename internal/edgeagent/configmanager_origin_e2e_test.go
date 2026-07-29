package edgeagent

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	origingovernance "goveto-edge/caddy/origingovernance"
	"goveto-edge/internal/edgeprotocol"
)

func TestAgentHTTPSOriginUsesSNIPrivateCAAndMTLS(t *testing.T) {
	ensureAgentLogSink(t)
	caCertificate, caPrivateKey, caPEM := createTestCertificateAuthority(t)
	serverCertificate, _ := createSignedCertificate(t, caCertificate, caPrivateKey, "tls.internal", true)
	clientCertificate, clientKeyPEM := createSignedCertificate(t, caCertificate, caPrivateKey, "edge-agent", false)
	clientCAPool := x509.NewCertPool()
	clientCAPool.AddCert(caCertificate)
	origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.TLS == nil || request.TLS.ServerName != "tls.internal" || len(request.TLS.PeerCertificates) == 0 {
			http.Error(w, "TLS policy missing", http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte("secure-origin"))
	}))
	origin.TLS = &tls.Config{
		Certificates: []tls.Certificate{serverCertificate},
		ClientAuth:   tls.RequireAndVerifyClientCert, ClientCAs: clientCAPool,
		MinVersion: tls.VersionTLS12,
	}
	origin.StartTLS()
	defer origin.Close()

	port := freePort(t)
	manager := NewConfigManager(filepath.Join(t.TempDir(), "sites.json"), ":"+strconv.Itoa(port))
	config := SiteConfig{
		SiteID: "private-origin", Version: 1, Domains: []string{"private.example.test"},
		Listener: ListenerConfig{HTTPEnabled: true, HTTPPort: port},
		Origins:  []OriginConfig{{Protocol: "https", Address: origin.Listener.Addr().String(), Weight: 1}},
		OriginPolicy: edgeprotocol.OriginPolicyConfig{
			TimeoutMS: 2000,
			ActiveHealth:  edgeprotocol.OriginActiveHealthConfig{Enabled: false},
			PassiveHealth: edgeprotocol.OriginPassiveHealthConfig{Enabled: false},
			Transport: edgeprotocol.OriginTransportConfig{
				DialTimeoutMS: 500, TLSHandshakeTimeoutMS: 500, ResponseHeaderTimeoutMS: 1000,
				IPVersion: "any", TLSServerName: "tls.internal", TLSRootCAPEM: []string{caPEM},
				TLSClientCertificatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientCertificate.Certificate[0]})),
				TLSClientPrivateKeyPEM:  clientKeyPEM,
			},
		},
	}
	if err := manager.ApplySite(config); err != nil {
		t.Fatalf("apply private origin: %v", err)
	}
	defer manager.Stop()
	request, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+"/", nil)
	request.Host = "private.example.test"
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK || string(body) != "secure-origin" {
		t.Fatalf("private origin response status=%d body=%q", response.StatusCode, body)
	}
}

func TestAgentDoesNotProbeOrigins(t *testing.T) {
	ensureAgentLogSink(t)
	var healthRequests atomic.Int64
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/ready" {
			healthRequests.Add(1)
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("primary"))
	}))
	defer primary.Close()
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/ready" {
			healthRequests.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = w.Write([]byte("backup"))
	}))
	defer backup.Close()

	manager, port := applyOriginGovernanceSite(t, primary, backup, edgeprotocol.OriginPolicyConfig{
		TimeoutMS: 2000,
		ActiveHealth: edgeprotocol.OriginActiveHealthConfig{
			Enabled: true, Method: http.MethodHead, ExpectedStatus: http.StatusNoContent,
			IntervalMS: 20, TimeoutMS: 100, Passes: 1, Fails: 1,
		},
		PassiveHealth: edgeprotocol.OriginPassiveHealthConfig{Enabled: false},
	})
	defer manager.Stop()

	time.Sleep(100 * time.Millisecond)
	if body, _ := requestOriginSite(t, port); body != "primary" {
		t.Fatalf("user request unexpectedly failed over: body=%q", body)
	}
	if healthRequests.Load() != 0 {
		t.Fatalf("origins received %d active health probes", healthRequests.Load())
	}
}

func TestAgentHTTPResponsesDoNotTripPassiveHealth(t *testing.T) {
	ensureAgentLogSink(t)
	origingovernance.ResetMetrics()
	t.Cleanup(origingovernance.ResetMetrics)
	var primaryHits atomic.Int64
	var backupHits atomic.Int64
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		primaryHits.Add(1)
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer primary.Close()
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backupHits.Add(1)
		_, _ = w.Write([]byte("backup"))
	}))
	defer backup.Close()

	manager, port := applyOriginGovernanceSite(t, primary, backup, edgeprotocol.OriginPolicyConfig{
		TimeoutMS: 2000,
		ActiveHealth: edgeprotocol.OriginActiveHealthConfig{Enabled: false},
		PassiveHealth: edgeprotocol.OriginPassiveHealthConfig{
			Enabled: true, FailDurationMS: 200, MaxFails: 1, UnhealthyStatus: []int{503},
		},
	})
	defer manager.Stop()

	for range 2 {
		if _, status := requestOriginSite(t, port); status != http.StatusNotFound {
			t.Fatalf("origin 404 was changed to status %d", status)
		}
	}
	metrics := map[string]origingovernance.Metric{}
	for _, metric := range origingovernance.SnapshotAndReset() {
		metrics[metric.OriginAddress] = metric
	}
	primaryMetric := metrics[primary.Listener.Addr().String()]
	if primaryMetric.Requests != 2 || primaryMetric.Errors != 0 || primaryHits.Load() != 2 || backupHits.Load() != 0 {
		t.Fatalf("HTTP response affected health: metric=%#v primary_hits=%d backup_hits=%d", primaryMetric, primaryHits.Load(), backupHits.Load())
	}
}

func TestAgentDoesNotRemoveSlowOrigin(t *testing.T) {
	ensureAgentLogSink(t)
	var primaryHits atomic.Int64
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		primaryHits.Add(1)
		time.Sleep(60 * time.Millisecond)
		_, _ = w.Write([]byte("primary"))
	}))
	defer primary.Close()
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("backup"))
	}))
	defer backup.Close()

	manager, port := applyOriginGovernanceSite(t, primary, backup, edgeprotocol.OriginPolicyConfig{
		TimeoutMS: 2000,
		ActiveHealth: edgeprotocol.OriginActiveHealthConfig{Enabled: false},
		PassiveHealth: edgeprotocol.OriginPassiveHealthConfig{
			Enabled: true, FailDurationMS: 500, MaxFails: 1, UnhealthyLatencyMS: 20,
		},
	})
	defer manager.Stop()

	if body, _ := requestOriginSite(t, port); body != "primary" {
		t.Fatalf("first slow response = %q", body)
	}
	if body, _ := requestOriginSite(t, port); body != "primary" || primaryHits.Load() != 2 {
		t.Fatalf("slow primary was removed: body=%q hits=%d", body, primaryHits.Load())
	}
}

func TestAgentTransportFailureFailsOverToBackup(t *testing.T) {
	ensureAgentLogSink(t)
	primary := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	primary.Close()
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("backup"))
	}))
	defer backup.Close()

	manager, port := applyOriginGovernanceSite(t, primary, backup, edgeprotocol.OriginPolicyConfig{
		TimeoutMS: 2000,
		PassiveHealth: edgeprotocol.OriginPassiveHealthConfig{
			Enabled: true, FailDurationMS: 500, MaxFails: 1,
		},
		Transport: edgeprotocol.OriginTransportConfig{
			DialTimeoutMS: 100, ResponseHeaderTimeoutMS: 500, IPVersion: "any",
		},
		Retry: edgeprotocol.OriginRetryConfig{Retries: 1, TryDurationMS: 1000, TryIntervalMS: 10},
	})
	defer manager.Stop()

	if body, status := requestOriginSite(t, port); body != "backup" || status != http.StatusOK {
		t.Fatalf("transport failure did not fail over: status=%d body=%q", status, body)
	}
	if body, status := requestOriginSite(t, port); body != "backup" || status != http.StatusOK {
		t.Fatalf("failed origin was not temporarily bypassed: status=%d body=%q", status, body)
	}
}

func TestAgentEnforcesTotalOriginTimeout(t *testing.T) {
	ensureAgentLogSink(t)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(500 * time.Millisecond)
		_, _ = w.Write([]byte("too late"))
	}))
	defer origin.Close()

	port := freePort(t)
	manager := NewConfigManager(filepath.Join(t.TempDir(), "sites.json"), ":"+strconv.Itoa(port))
	config := SiteConfig{
		SiteID: "origin-timeout", Version: 1, Domains: []string{"timeout.example.test"},
		Listener: ListenerConfig{HTTPEnabled: true, HTTPPort: port},
		Origins:  []OriginConfig{{Protocol: "http", Address: origin.Listener.Addr().String(), Weight: 1}},
		OriginPolicy: edgeprotocol.OriginPolicyConfig{
			TimeoutMS: 50,
			ActiveHealth:  edgeprotocol.OriginActiveHealthConfig{Enabled: false},
			PassiveHealth: edgeprotocol.OriginPassiveHealthConfig{Enabled: false},
			Retry:         edgeprotocol.OriginRetryConfig{Retries: 1, TryDurationMS: 100, TryIntervalMS: 10},
		},
	}
	if err := manager.ApplySite(config); err != nil {
		t.Fatalf("apply timeout site: %v", err)
	}
	defer manager.Stop()
	request, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+"/", nil)
	request.Host = "timeout.example.test"
	start := time.Now()
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if elapsed := time.Since(start); elapsed > 300*time.Millisecond {
		t.Fatalf("total origin timeout took %v", elapsed)
	}
	if response.StatusCode < 500 {
		t.Fatalf("timed out origin returned status %d", response.StatusCode)
	}
}

func applyOriginGovernanceSite(t *testing.T, primary, backup *httptest.Server, policy edgeprotocol.OriginPolicyConfig) (*ConfigManager, int) {
	t.Helper()
	port := freePort(t)
	manager := NewConfigManager(filepath.Join(t.TempDir(), "sites.json"), ":"+strconv.Itoa(port))
	config := SiteConfig{
		SiteID: "governance-e2e", Version: 1, Domains: []string{"governance.example.test"},
		Listener: ListenerConfig{HTTPEnabled: true, HTTPPort: port},
		Origins: []OriginConfig{
			{Protocol: "http", Address: primary.Listener.Addr().String(), Weight: 1},
			{Protocol: "http", Address: backup.Listener.Addr().String(), Weight: 1, Priority: 10},
		},
		OriginPolicy: policy,
	}
	if err := manager.ApplySite(config); err != nil {
		t.Fatalf("apply site: %v", err)
	}
	return manager, port
}

func requestOriginSite(t *testing.T, port int) (string, int) {
	t.Helper()
	request, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+"/", nil)
	request.Host = "governance.example.test"
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body), response.StatusCode
}

func createTestCertificateAuthority(t *testing.T) (*x509.Certificate, ed25519.PrivateKey, string) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Goveto Test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return certificate, privateKey, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func createSignedCertificate(t *testing.T, ca *x509.Certificate, caPrivateKey ed25519.PrivateKey, name string, server bool) (tls.Certificate, string) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature,
	}
	if server {
		template.DNSNames = []string{name}
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	} else {
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, publicKey, caPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	privateKeyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKeyDER})
	certificate, err := tls.X509KeyPair(certificatePEM, privateKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return certificate, string(privateKeyPEM)
}
