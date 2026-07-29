package analytics

import "testing"

func TestNormalizeLegacyRuntimeMetricUsesEmptyCacheAlerts(t *testing.T) {
	metric := normalizeRuntimeMetric(NodeRuntimeMetric{})
	if metric.CacheAlerts == nil || len(metric.CacheAlerts) != 0 {
		t.Fatalf("legacy cache alerts were not normalized: %#v", metric.CacheAlerts)
	}

	existing := []string{"CAPACITY_HIGH"}
	metric = normalizeRuntimeMetric(NodeRuntimeMetric{CacheAlerts: existing})
	if len(metric.CacheAlerts) != 1 || metric.CacheAlerts[0] != existing[0] {
		t.Fatalf("existing cache alerts changed: %#v", metric.CacheAlerts)
	}
}

func TestAccessLogHasTimestamp(t *testing.T) {
	if !accessLogHasTimestamp([]byte(`{"ts":1753783200.25}`)) {
		t.Fatal("valid access-log timestamp was not detected")
	}
	if accessLogHasTimestamp([]byte(`{"status":200}`)) || accessLogHasTimestamp([]byte(`not-json`)) {
		t.Fatal("missing access-log timestamp was detected")
	}
}

func TestDecodeOriginHealth(t *testing.T) {
	metric, ok := decodeOriginHealth([]byte(`{
		"minute":"2026-07-25T12:00:00Z","site_id":"site-1","origin_address":"origin:443",
		"healthy":true,"available":false,"fails":3,"requests":10,"errors":2,
		"average_latency_ms":25.5,"error_rate":0.2
	}`), "cluster-1", "node-1")
	if !ok {
		t.Fatal("valid origin metric was rejected")
	}
	if metric.ClusterID != "cluster-1" || metric.NodeID != "node-1" || metric.SiteID != "site-1" ||
		metric.OriginAddress != "origin:443" || !metric.Healthy || metric.Available || metric.Fails != 3 ||
		metric.Requests != 10 || metric.Errors != 2 || metric.AverageLatencyMS != 25.5 || metric.ErrorRate != 0.2 {
		t.Fatalf("decoded metric = %#v", metric)
	}
}

func TestDecodeOriginHealthRejectsIncompletePayload(t *testing.T) {
	if _, ok := decodeOriginHealth([]byte(`{"site_id":"site-1"}`), "cluster-1", "node-1"); ok {
		t.Fatal("incomplete metric was accepted")
	}
}
