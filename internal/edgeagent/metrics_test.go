package edgeagent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	gnet "github.com/shirou/gopsutil/v4/net"
	origingovernance "goveto-edge/caddy/origingovernance"
)

func TestNodeConfigStorePersistsAndNormalizes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.json")
	store := NewNodeConfigStore(path)
	if err := store.Set(NodeConfig{MaxDiskUsagePercent: 0, MaxSizeBytes: 42}); err != nil {
		t.Fatal(err)
	}
	got := store.Get()
	if got.CacheDirectory != "/opt/goveto-edge/cache" {
		t.Fatalf("default cache dir missing: %#v", got)
	}
	if got.MaxDiskUsagePercent != 80 {
		t.Fatalf("percent not normalized: %#v", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("permissions: %o", info.Mode().Perm())
	}
	reloaded := NewNodeConfigStore(path)
	if reloaded.Get().MaxSizeBytes != 42 {
		t.Fatalf("reload failed: %#v", reloaded.Get())
	}
}

func TestAppendMetricsWritesRuntimeRecord(t *testing.T) {
	origingovernance.ResetMetrics()
	queue, err := OpenLogQueue(filepath.Join(t.TempDir(), "logs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	cacheDirectory := t.TempDir()
	if err := os.WriteFile(filepath.Join(cacheDirectory, "cached-response"), []byte("12345"), 0600); err != nil {
		t.Fatal(err)
	}
	appendMetrics(queue, NodeConfig{
		CacheDirectory:      cacheDirectory,
		AutoMaxSize:         true,
		MaxDiskUsagePercent: 80,
	})
	batch, err := queue.Batch(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 1 || batch[0].Type != "node_runtime" {
		t.Fatalf("unexpected metrics batch: %#v", batch)
	}
	var payload map[string]any
	if err := json.Unmarshal(batch[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"minute", "cpu_usage_percent", "memory_used_bytes", "load_1", "connections", "cache_directory", "cache_used_bytes"} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("missing metrics key %q in %#v", key, payload)
		}
	}
	if payload["cache_used_bytes"] != float64(5) {
		t.Fatalf("cache directory size = %#v, want 5", payload["cache_used_bytes"])
	}
	if _, ok := payload["cache_max_bytes"]; ok {
		t.Fatalf("runtime metrics must not expose cache max size: %#v", payload)
	}
}

func TestAppendOriginMetricWritesHealthLatencyAndErrorRate(t *testing.T) {
	queue, err := OpenLogQueue(filepath.Join(t.TempDir(), "logs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	appendOriginMetric(queue, time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC), origingovernance.Metric{
		SiteID: "site-1", OriginAddress: "origin:443", Healthy: true, Available: true,
		Fails: 1, Requests: 20, Errors: 2, AverageLatencyMS: 12.5, ErrorRate: 0.1,
	})
	batch, err := queue.Batch(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 1 || batch[0].Type != "origin_health" {
		t.Fatalf("unexpected origin metric batch: %#v", batch)
	}
	var payload map[string]any
	if err := json.Unmarshal(batch[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["site_id"] != "site-1" || payload["origin_address"] != "origin:443" || payload["error_rate"] != 0.1 {
		t.Fatalf("origin metric payload = %#v", payload)
	}
}

func TestEstablishedConnections(t *testing.T) {
	connections := []gnet.ConnectionStat{
		{Status: "ESTABLISHED"},
		{Status: "LISTEN"},
		{Status: "ESTABLISHED"},
		{Status: "TIME_WAIT"},
	}
	if got := establishedConnections(connections); got != 2 {
		t.Fatalf("established connections = %d, want 2", got)
	}
}

func TestAppendMetricsReportsMissingDisk(t *testing.T) {
	queue, err := OpenLogQueue(filepath.Join(t.TempDir(), "logs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	appendMetrics(queue, NodeConfig{
		CacheDirectory: filepath.Join(t.TempDir(), "missing-cache-dir"),
		AutoMaxSize:    true,
	})
	batch, err := queue.Batch(1)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(batch[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["disk_error"] == nil || payload["cache_error"] == nil {
		t.Fatalf("expected disk and cache errors in %#v", payload)
	}
}
