package edgeagent

import (
	"crypto/sha256"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	cachefs "goveto-edge/caddy/simplefs"
	policy "goveto-edge/internal/policy"
)

func TestDeliveryPoolsAndSplitsUseIsolatedCaches(t *testing.T) {
	ensureAgentLogSink(t)
	var baseCalls, canaryCalls atomic.Int32
	origin := func(name string, calls *atomic.Int32) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); _, _ = io.WriteString(w, name) }))
	}
	base, canary := origin("base", &baseCalls), origin("canary", &canaryCalls)
	defer base.Close()
	defer canary.Close()
	port := freePort(t)
	dir := t.TempDir()
	t.Cleanup(cachefs.OverrideDiskUsageForTesting(dir, 1<<40, 0))
	manager := NewConfigManager(filepath.Join(t.TempDir(), "sites.json"), ":"+strconv.Itoa(port))
	manager.nodeConfig = NodeConfig{CacheDirectory: dir, MaxSizeBytes: 64 << 20, MaxDiskUsagePercent: 90}
	defer manager.Stop()
	delivery := policy.DefaultDeliveryPolicy()
	delivery.OriginPools = []policy.PathOriginPool{
		{Name: "base", Paths: []string{"/assets/*"}, Origins: []policy.DeliveryOrigin{{Protocol: "http", Address: strings.TrimPrefix(base.URL, "http://")}}},
		{Name: "canary", Paths: []string{"/assets/*"}, Origins: []policy.DeliveryOrigin{{Protocol: "http", Address: strings.TrimPrefix(canary.URL, "http://")}}},
	}
	delivery.Splits = []policy.TrafficSplitRule{{Name: "canary", Pool: "canary", HeaderName: "X-Canary", Value: "yes"}}
	config := SiteConfig{SiteID: "delivery-cache", Version: 1, Domains: []string{"delivery.test"}, Listener: ListenerConfig{HTTPEnabled: true, HTTPPort: port}, Origins: []OriginConfig{{Protocol: "http", Address: strings.TrimPrefix(base.URL, "http://")}}, Cache: toMap(t, cachePolicyWithCatchAllRule()), Delivery: toMap(t, delivery)}
	if err := manager.ApplySite(config); err != nil {
		t.Fatal(err)
	}
	client := edgeTestClient
	for _, group := range []struct {
		canary bool
		body   string
	}{{false, "base"}, {true, "canary"}} {
		for index := range 2 {
			request, _ := http.NewRequest("GET", "http://127.0.0.1:"+strconv.Itoa(port)+"/assets/app.js", nil)
			request.Host = "delivery.test"
			if group.canary {
				request.Header.Set("X-Canary", "yes")
			}
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			want := "MISS"
			if index == 1 {
				want = "HIT"
			}
			if string(body) != group.body || response.Header.Get("X-Cache") != want {
				t.Fatalf("body=%q cache=%q", body, response.Header.Get("X-Cache"))
			}
		}
	}
	if baseCalls.Load() != 1 || canaryCalls.Load() != 1 {
		t.Fatalf("origin calls base=%d canary=%d", baseCalls.Load(), canaryCalls.Load())
	}
}

func TestDeliveryPercentageSplitUsesIsolatedCache(t *testing.T) {
	ensureAgentLogSink(t)
	var baseCalls, canaryCalls atomic.Int32
	origin := func(name string, calls *atomic.Int32) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); _, _ = io.WriteString(w, name) }))
	}
	base, canary := origin("base", &baseCalls), origin("canary", &canaryCalls)
	defer base.Close()
	defer canary.Close()
	port := freePort(t)
	dir := t.TempDir()
	t.Cleanup(cachefs.OverrideDiskUsageForTesting(dir, 1<<40, 0))
	manager := NewConfigManager(filepath.Join(t.TempDir(), "sites.json"), ":"+strconv.Itoa(port))
	manager.nodeConfig = NodeConfig{CacheDirectory: dir, MaxSizeBytes: 64 << 20, MaxDiskUsagePercent: 90}
	defer manager.Stop()
	delivery := policy.DefaultDeliveryPolicy()
	delivery.OriginPools = []policy.PathOriginPool{
		{Name: "base", Paths: []string{"/assets/*"}, Origins: []policy.DeliveryOrigin{{Protocol: "http", Address: strings.TrimPrefix(base.URL, "http://")}}},
		{Name: "canary", Paths: []string{"/assets/*"}, Origins: []policy.DeliveryOrigin{{Protocol: "http", Address: strings.TrimPrefix(canary.URL, "http://")}}},
	}
	delivery.Splits = []policy.TrafficSplitRule{{Name: "canary", Pool: "canary", Percentage: 50}}
	config := SiteConfig{SiteID: "delivery-percentage", Version: 1, Domains: []string{"percentage.test"}, Listener: ListenerConfig{HTTPEnabled: true, HTTPPort: port}, Origins: []OriginConfig{{Protocol: "http", Address: strings.TrimPrefix(base.URL, "http://")}}, Cache: toMap(t, cachePolicyWithCatchAllRule()), Delivery: toMap(t, delivery)}
	if err := manager.ApplySite(config); err != nil {
		t.Fatal(err)
	}
	// A percentage split decides from the client identity, not a request
	// header, so both groups share the same URL; only key_namespace keeps the
	// split and pool caches from serving each other's entries.
	salt := config.SiteID + ":" + delivery.Splits[0].Name
	client := edgeTestClient
	for _, group := range []struct {
		agent string
		body  string
	}{
		{percentageIdentity(t, salt, 50, true), "canary"},
		{percentageIdentity(t, salt, 50, false), "base"},
	} {
		for index := range 2 {
			request, _ := http.NewRequest("GET", "http://127.0.0.1:"+strconv.Itoa(port)+"/assets/app.js", nil)
			request.Host = "percentage.test"
			request.Header.Set("User-Agent", group.agent)
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			want := "MISS"
			if index == 1 {
				want = "HIT"
			}
			if string(body) != group.body || response.Header.Get("X-Cache") != want {
				t.Fatalf("agent=%q body=%q cache=%q", group.agent, body, response.Header.Get("X-Cache"))
			}
		}
	}
	if baseCalls.Load() != 1 || canaryCalls.Load() != 1 {
		t.Fatalf("origin calls base=%d canary=%d", baseCalls.Load(), canaryCalls.Load())
	}
}

// percentageIdentity finds a User-Agent whose client identity (loopback IP +
// User-Agent, matching splitmatch.clientIdentity) lands inside or outside the
// given percentage for the split salt.
func percentageIdentity(t *testing.T, salt string, percentage int, match bool) string {
	t.Helper()
	for index := 0; ; index++ {
		agent := "agent-" + strconv.Itoa(index)
		digest := sha256.Sum256([]byte(salt + "\x00" + "127.0.0.1\x00" + agent))
		if (int(binary.BigEndian.Uint32(digest[:4])%100) < percentage) == match {
			return agent
		}
	}
}
