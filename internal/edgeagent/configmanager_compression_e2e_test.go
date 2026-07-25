package edgeagent

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"

	compressionpolicy "goveto-edge/internal/policy"
)

func TestAgentCompressionEndToEnd(t *testing.T) {
	ensureAgentLogSink(t)
	body := bytes.Repeat([]byte("edge compression response "), 32)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write(body)
	}))
	defer origin.Close()

	port := freePort(t)
	manager := NewConfigManager(filepath.Join(t.TempDir(), "sites.json"), ":"+strconv.Itoa(freePort(t)))
	t.Cleanup(func() { _ = manager.Stop() })

	policy := compressionpolicy.DefaultCompressionPolicy()
	policy.Enabled = true
	policy.MinimumLength = 16
	policy.MaximumLength = 1 << 20
	policy.ExcludedPaths = []string{"/skip"}
	config := SiteConfig{
		SiteID:      "site-compression",
		Version:     1,
		Domains:     []string{"compression.example.test"},
		Listener:    ListenerConfig{HTTPEnabled: true, HTTPPort: port},
		Origins:     []OriginConfig{{Protocol: "http", Address: strings.TrimPrefix(origin.URL, "http://")}},
		Compression: toMap(t, policy),
	}
	if err := manager.ApplySite(config); err != nil {
		t.Fatalf("apply site: %v", err)
	}

	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	request, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+"/index.html", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = config.Domains[0]
	request.Header.Set("Accept-Encoding", "gzip, br")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if got := response.Header.Get("Content-Encoding"); got != "br" {
		t.Fatalf("Content-Encoding=%q", got)
	}
	decoded, err := io.ReadAll(brotli.NewReader(response.Body))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, body) {
		t.Fatal("decoded response did not match origin")
	}

	skipped, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+"/skip/file.html", nil)
	if err != nil {
		t.Fatal(err)
	}
	skipped.Host = config.Domains[0]
	skipped.Header.Set("Accept-Encoding", "br")
	skippedResponse, err := client.Do(skipped)
	if err != nil {
		t.Fatal(err)
	}
	defer skippedResponse.Body.Close()
	if got := skippedResponse.Header.Get("Content-Encoding"); got != "" {
		t.Fatalf("excluded path Content-Encoding=%q", got)
	}
	skippedBody, err := io.ReadAll(skippedResponse.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(skippedBody, body) {
		t.Fatal("excluded path response changed")
	}
}
