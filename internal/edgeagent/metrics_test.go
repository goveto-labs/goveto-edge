package edgeagent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	gnet "github.com/shirou/gopsutil/v4/net"
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
	queue, err := OpenLogQueue(filepath.Join(t.TempDir(), "logs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	appendMetrics(queue, NodeConfig{
		CacheDirectory:      t.TempDir(),
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
	for _, key := range []string{"minute", "cpu_usage_percent", "memory_used_bytes", "load_1", "connections", "cache_directory", "cache_max_bytes"} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("missing metrics key %q in %#v", key, payload)
		}
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
	if payload["disk_error"] == nil {
		t.Fatalf("expected disk_error in %#v", payload)
	}
}
