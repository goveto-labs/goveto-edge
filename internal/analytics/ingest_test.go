package analytics

import (
	"math"
	"testing"
	"time"

	"goveto-edge/internal/edgeprotocol"
)

const (
	testSiteID      = "f47ac10b-58cc-4372-a567-0e02b2c3d479"
	testOtherSiteID = "550e8400-e29b-41d4-a716-446655440000"
)

func TestAccessLogHasTimestamp(t *testing.T) {
	if !accessLogHasTimestamp([]byte(`{"ts":1753783200.25}`)) {
		t.Fatal("valid access-log timestamp was not detected")
	}
	if accessLogHasTimestamp([]byte(`{"status":200}`)) || accessLogHasTimestamp([]byte(`not-json`)) {
		t.Fatal("missing access-log timestamp was detected")
	}
}

func TestMetricRecordTypeBoundsLabels(t *testing.T) {
	for _, recordType := range []string{"access", "caddy", "node_runtime", "origin_health"} {
		if got := metricRecordType(recordType); got != recordType {
			t.Errorf("metricRecordType(%q) = %q", recordType, got)
		}
	}
	if got := metricRecordType("arbitrary-agent-value"); got != "unknown" {
		t.Errorf("metricRecordType(arbitrary) = %q, want unknown", got)
	}
}

func TestDecodeOriginHealth(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 2, 0, 0, time.UTC)
	metric, ok := decodeOriginHealth([]byte(`{
		"minute":"2026-07-25T12:00:00Z","site_id":"`+testSiteID+`","origin_address":"origin:443",
		"healthy":true,"available":false,"fails":3,"requests":10,"errors":2,
		"average_latency_ms":25.5,"error_rate":0.2
	}`), "cluster-1", "node-1", now)
	if !ok {
		t.Fatal("valid origin metric was rejected")
	}
	if metric.ClusterID != "cluster-1" || metric.NodeID != "node-1" || metric.SiteID != testSiteID ||
		metric.OriginAddress != "origin:443" || !metric.Healthy || metric.Available || metric.Fails != 3 ||
		metric.Requests != 10 || metric.Errors != 2 || metric.AverageLatencyMS != 25.5 || metric.ErrorRate != 0.2 {
		t.Fatalf("decoded metric = %#v", metric)
	}
}

func TestDecodeOriginHealthRejectsIncompletePayload(t *testing.T) {
	if _, ok := decodeOriginHealth([]byte(`{"site_id":"`+testSiteID+`"}`), "cluster-1", "node-1", time.Now()); ok {
		t.Fatal("incomplete metric was accepted")
	}
}

func TestDecodeOriginHealthRejectsInvalidValues(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 2, 0, 0, time.UTC)
	tests := map[string]string{
		"malformed site ID": `{"minute":"2026-07-25T12:00:00Z","site_id":"not-a-uuid","origin_address":"origin:443"}`,
		"old minute":        `{"minute":"2026-06-20T12:00:00Z","site_id":"` + testSiteID + `","origin_address":"origin:443"}`,
		"future minute":     `{"minute":"2026-07-25T12:08:00Z","site_id":"` + testSiteID + `","origin_address":"origin:443"}`,
		"sub-minute time":   `{"minute":"2026-07-25T12:00:01Z","site_id":"` + testSiteID + `","origin_address":"origin:443"}`,
		"negative fails":    `{"minute":"2026-07-25T12:00:00Z","site_id":"` + testSiteID + `","origin_address":"origin:443","fails":-1}`,
		"errors exceed requests": `{"minute":"2026-07-25T12:00:00Z","site_id":"` + testSiteID +
			`","origin_address":"origin:443","requests":1,"errors":2}`,
		"errors without requests": `{"minute":"2026-07-25T12:00:00Z","site_id":"` + testSiteID +
			`","origin_address":"origin:443","errors":1}`,
		"negative latency": `{"minute":"2026-07-25T12:00:00Z","site_id":"` + testSiteID +
			`","origin_address":"origin:443","requests":1,"average_latency_ms":-1}`,
		"excessive latency": `{"minute":"2026-07-25T12:00:00Z","site_id":"` + testSiteID +
			`","origin_address":"origin:443","requests":1,"average_latency_ms":86400001}`,
		"invalid error rate": `{"minute":"2026-07-25T12:00:00Z","site_id":"` + testSiteID +
			`","origin_address":"origin:443","requests":1,"error_rate":1.1}`,
		"latency without requests": `{"minute":"2026-07-25T12:00:00Z","site_id":"` + testSiteID +
			`","origin_address":"origin:443","average_latency_ms":1}`,
		"invalid address": `{"minute":"2026-07-25T12:00:00Z","site_id":"` + testSiteID +
			`","origin_address":"origin without port"}`,
		"address with control character": `{"minute":"2026-07-25T12:00:00Z","site_id":"` + testSiteID +
			`","origin_address":"origin\u0000:443"}`,
		"unsupported network prefix": `{"minute":"2026-07-25T12:00:00Z","site_id":"` + testSiteID +
			`","origin_address":"udp/origin:443"}`,
	}
	for name, payload := range tests {
		t.Run(name, func(t *testing.T) {
			if _, ok := decodeOriginHealth([]byte(payload), "cluster-1", "node-1", now); ok {
				t.Fatal("invalid metric was accepted")
			}
		})
	}
}

func TestDecodeOriginHealthCanonicalizesSiteID(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 2, 0, 0, time.UTC)
	metric, ok := decodeOriginHealth([]byte(`{
		"minute":"2026-07-25T12:00:00Z","site_id":"F47AC10B-58CC-4372-A567-0E02B2C3D479",
		"origin_address":"origin:443"
	}`), "cluster-1", "node-1", now)
	if !ok || metric.SiteID != testSiteID {
		t.Fatalf("decoded metric = %#v, ok = %v", metric, ok)
	}
}

func TestValidOriginHealthAddressAcceptsGeneratedDialFormats(t *testing.T) {
	for _, address := range []string{"origin.example:443", "[2001:db8::1]:443", "tcp4/origin.example:80", "tcp6/[2001:db8::1]:443"} {
		if !validOriginHealthAddress(address) {
			t.Fatalf("generated dial address %q was rejected", address)
		}
	}
}

func TestValidOriginHealthMetricRejectsNonFiniteValues(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 2, 0, 0, time.UTC)
	base := OriginHealthMetric{
		Minute: now.Truncate(time.Minute), SiteID: testSiteID, OriginAddress: "origin:443", Requests: 1,
	}
	for name, value := range map[string]float64{"NaN": math.NaN(), "+Inf": math.Inf(1), "-Inf": math.Inf(-1)} {
		t.Run("latency "+name, func(t *testing.T) {
			metric := base
			metric.AverageLatencyMS = value
			if validOriginHealthMetric(&metric, now) {
				t.Fatal("non-finite latency was accepted")
			}
		})
		t.Run("rate "+name, func(t *testing.T) {
			metric := base
			metric.ErrorRate = value
			if validOriginHealthMetric(&metric, now) {
				t.Fatal("non-finite error rate was accepted")
			}
		})
	}
}

func TestDecodeOriginHealthBatchCountsRejectedRecords(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 2, 0, 0, time.UTC)
	records := []edgeprotocol.LogRecord{
		{Type: "access", Payload: []byte(`{}`)},
		{Type: "origin_health", Payload: []byte(`{"minute":"2026-07-25T12:00:00Z","site_id":"` + testSiteID + `","origin_address":"origin:443"}`)},
		{Type: "origin_health", Payload: []byte(`{"minute":"2026-07-25T12:00:00Z","site_id":"bad","origin_address":"origin:443"}`)},
	}
	metrics, rejected := decodeOriginHealthBatch(records, "cluster-1", "node-1", now)
	if len(metrics) != 1 || rejected != 1 {
		t.Fatalf("metrics = %d, rejected = %d", len(metrics), rejected)
	}
}

func TestIntersectOriginHealthSitesRequiresClusterAndNodeAuthorization(t *testing.T) {
	got := intersectOriginHealthSites(
		map[string]bool{testSiteID: true, testOtherSiteID: true},
		map[string]bool{testSiteID: true, "6ba7b810-9dad-11d1-80b4-00c04fd430c8": true},
	)
	if len(got) != 1 || !got[testSiteID] {
		t.Fatalf("authorized sites = %#v", got)
	}
}
