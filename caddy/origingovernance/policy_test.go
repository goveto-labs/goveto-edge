package origingovernance

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp/reverseproxy"
)

func TestSelectionPolicyUsesWeightsAndSetsPerOriginHost(t *testing.T) {
	policy := &SelectionPolicy{
		SiteID: "site-1", Scheduler: "round_robin",
		Backends: []Backend{
			{Dial: "one.test:80", HostHeader: "one.internal", Weight: 1},
			{Dial: "two.test:80", HostHeader: "two.internal", Weight: 3},
		},
	}
	if err := policy.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}
	pool := reverseproxy.UpstreamPool{
		{Dial: "one.test:80", Host: new(reverseproxy.Host)},
		{Dial: "two.test:80", Host: new(reverseproxy.Host)},
	}
	counts := map[string]int{}
	for range 8 {
		replacer := caddy.NewReplacer()
		request := httptest.NewRequest("GET", "http://site.test/", nil)
		request = request.WithContext(context.WithValue(request.Context(), caddy.ReplacerCtxKey, replacer))
		selected := policy.Select(pool, request, nil)
		counts[selected.Dial]++
		host, _ := replacer.GetString("goveto.origin.host")
		if selected.Dial == "one.test:80" && host != "one.internal" {
			t.Fatalf("first origin host = %q", host)
		}
		if selected.Dial == "two.test:80" && host != "two.internal" {
			t.Fatalf("second origin host = %q", host)
		}
	}
	if counts["one.test:80"] != 2 || counts["two.test:80"] != 6 {
		t.Fatalf("weighted selections = %#v", counts)
	}
}

func TestSelectionPolicyKeepsBackupIdleWhilePrimaryAvailable(t *testing.T) {
	policy := &SelectionPolicy{SiteID: "site-1", Backends: []Backend{
		{Dial: "primary:80", Weight: 1, Priority: 0},
		{Dial: "backup:80", Weight: 100, Priority: 10},
	}}
	if err := policy.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}
	pool := reverseproxy.UpstreamPool{
		{Dial: "primary:80", Host: new(reverseproxy.Host)},
		{Dial: "backup:80", Host: new(reverseproxy.Host)},
	}
	request := httptest.NewRequest("GET", "http://site.test/", nil)
	request = request.WithContext(context.WithValue(request.Context(), caddy.ReplacerCtxKey, caddy.NewReplacer()))
	if selected := policy.Select(pool, request, nil); selected.Dial != "primary:80" {
		t.Fatalf("selected %q while primary was available", selected.Dial)
	}
}

func TestSelectionPolicyUsesAvailableBackup(t *testing.T) {
	policy := &SelectionPolicy{SiteID: "site-1", Backends: []Backend{
		{Dial: "primary:80", Priority: 0},
		{Dial: "backup:80", Priority: 10},
	}}
	policy.available = func(upstream *reverseproxy.Upstream) bool { return upstream.Dial == "backup:80" }
	if err := policy.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}
	pool := reverseproxy.UpstreamPool{
		{Dial: "primary:80", Host: new(reverseproxy.Host)},
		{Dial: "backup:80", Host: new(reverseproxy.Host)},
	}
	request := httptest.NewRequest("GET", "http://site.test/", nil)
	if selected := policy.Select(pool, request, nil); selected == nil || selected.Dial != "backup:80" {
		t.Fatalf("selected %#v, want available backup", selected)
	}
}

func TestSelectionPolicyStillUsesSingleUnavailableOrigin(t *testing.T) {
	policy := &SelectionPolicy{SiteID: "site-1", Backends: []Backend{{Dial: "origin:80"}}}
	policy.available = func(*reverseproxy.Upstream) bool { return false }
	if err := policy.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}
	pool := reverseproxy.UpstreamPool{{Dial: "origin:80", Host: new(reverseproxy.Host)}}
	request := httptest.NewRequest("GET", "http://site.test/", nil)
	if selected := policy.Select(pool, request, nil); selected == nil || selected.Dial != "origin:80" {
		t.Fatalf("selected %#v, want configured origin", selected)
	}
}

func TestSelectionPolicyFallsBackToLowestPriorityWhenAllUnavailable(t *testing.T) {
	policy := &SelectionPolicy{SiteID: "site-1", Backends: []Backend{
		{Dial: "primary-a:80", Priority: 0},
		{Dial: "primary-b:80", Priority: 0},
		{Dial: "backup:80", Priority: 10},
	}}
	policy.available = func(*reverseproxy.Upstream) bool { return false }
	if err := policy.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}
	pool := reverseproxy.UpstreamPool{
		{Dial: "primary-a:80", Host: new(reverseproxy.Host)},
		{Dial: "primary-b:80", Host: new(reverseproxy.Host)},
		{Dial: "backup:80", Host: new(reverseproxy.Host)},
	}
	request := httptest.NewRequest("GET", "http://site.test/", nil)
	selected := policy.Select(pool, request, nil)
	if selected == nil || selected.Dial == "backup:80" {
		t.Fatalf("selected %#v, want an unavailable primary", selected)
	}
}

func TestOriginMetricsSnapshotReportsLatencyAndErrorRate(t *testing.T) {
	ResetMetrics()
	t.Cleanup(ResetMetrics)
	updateHealth("site-1", "origin:80", true, true, 0)
	observe("site-1", "origin:80", 10*time.Millisecond, false)
	observe("site-1", "origin:80", 30*time.Millisecond, true)
	metrics := SnapshotAndReset()
	if len(metrics) != 1 {
		t.Fatalf("metrics = %#v", metrics)
	}
	metric := metrics[0]
	if !metric.Healthy || !metric.Available || metric.Requests != 2 || metric.Errors != 1 {
		t.Fatalf("metric = %#v", metric)
	}
	if metric.AverageLatencyMS != 20 || metric.ErrorRate != 0.5 {
		t.Fatalf("latency/error rate = %v/%v", metric.AverageLatencyMS, metric.ErrorRate)
	}
}

func TestHTTPTransportRequiresCompleteMTLSKeyPair(t *testing.T) {
	transport := &HTTPTransport{ClientCertificatePEM: "certificate only"}
	if err := transport.Provision(caddy.Context{}); err == nil {
		t.Fatal("expected incomplete mTLS key pair to fail")
	}
}
