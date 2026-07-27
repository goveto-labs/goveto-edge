package edgeagent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/disk"
	gnet "github.com/shirou/gopsutil/v4/net"
	origingovernance "goveto-edge/caddy/origingovernance"
	cachefs "goveto-edge/caddy/simplefs"
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
	if err := store.Set(NodeConfig{MaxDiskUsagePercent: 95, MaxSizeBytes: 42}); err != nil {
		t.Fatal(err)
	}
	if got := store.Get().MaxDiskUsagePercent; got != 90 {
		t.Fatalf("hard disk limit=%d, want 90", got)
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
	legacyPath := filepath.Join(t.TempDir(), "legacy-node.json")
	if err := os.WriteFile(legacyPath, []byte(`{"cache_directory":"/cache","max_disk_usage_percent":95}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := NewNodeConfigStore(legacyPath).Get().MaxDiskUsagePercent; got != 90 {
		t.Fatalf("legacy hard disk limit=%d, want 90", got)
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
	for _, key := range []string{
		"minute", "cpu_usage_percent", "memory_used_bytes", "load_1", "connections",
		"cache_directory", "cache_used_bytes", "cache_entries", "cache_hits", "cache_misses",
		"cache_evictions", "cache_rejected_writes", "cache_corruptions", "cache_hit_rate",
		"cache_capacity_ratio", "cache_alerts",
	} {
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

func TestCacheAlertsCoverCapacityEvictionFailureCorruptionAndHitRate(t *testing.T) {
	stats := cachefs.Statistics{
		Hits: 20, Misses: 80, HitRate: 0.2, Evictions: 1, RejectedWrites: 2, Corruptions: 3,
	}
	got := cacheAlerts(stats, 0.95)
	want := []string{"CAPACITY_HIGH", "EVICTIONS", "WRITE_REJECTED", "CORRUPTION_RECOVERED", "HIT_RATE_LOW"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cache alerts = %#v, want %#v", got, want)
	}
	if ratio := cacheCapacityRatio(NodeConfig{MaxSizeBytes: 100}, 75, nil); ratio != 0.75 {
		t.Fatalf("fixed cache capacity ratio = %v", ratio)
	}
	if ratio := cacheCapacityRatio(
		NodeConfig{MaxSizeBytes: 1000, MaxDiskUsagePercent: 50},
		100,
		&disk.UsageStat{Total: 100, Used: 45},
	); ratio != 0.9 {
		t.Fatalf("filesystem-dominated fixed cache capacity ratio = %v, want 0.9", ratio)
	}
}

func TestCacheEventAlertsUseActivitySincePreviousSample(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache")
	current := cachefs.Statistics{Hits: 20, Misses: 80, HitRate: 0.2, Evictions: 1, RejectedWrites: 1}
	first := cacheActivitySinceLast(path, current)
	if got := cacheAlerts(first, 0); !reflect.DeepEqual(got, []string{"EVICTIONS", "WRITE_REJECTED", "HIT_RATE_LOW"}) {
		t.Fatalf("first activity alerts=%v", got)
	}
	unchanged := cacheActivitySinceLast(path, current)
	if got := cacheAlerts(unchanged, 0); len(got) != 0 {
		t.Fatalf("unchanged cumulative counters kept alerts active: %v", got)
	}
	if got := normalizeCacheDiskPercent(95); got != 90 {
		t.Fatalf("normalizeCacheDiskPercent(95)=%d, want 90", got)
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
