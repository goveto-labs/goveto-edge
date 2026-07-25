package edgeagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	origingovernance "goveto-edge/caddy/origingovernance"
	cachefs "goveto-edge/caddy/simplefs"
	"goveto-edge/internal/edgeprotocol"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	gnet "github.com/shirou/gopsutil/v4/net"
)

type NodeConfig = edgeprotocol.NodeCacheConfig

type NodeConfigStore struct {
	mu    sync.RWMutex
	value NodeConfig
	path  string
}

func NewNodeConfigStore(path string) *NodeConfigStore {
	store := &NodeConfigStore{path: path, value: NodeConfig{CacheDirectory: "/opt/goveto-edge/cache", AutoMaxSize: true, MaxDiskUsagePercent: 80}}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &store.value)
	}
	return store
}
func (s *NodeConfigStore) Set(value NodeConfig) error {
	if value.CacheDirectory == "" {
		value.CacheDirectory = "/opt/goveto-edge/cache"
	}
	if value.MaxDiskUsagePercent < 1 || value.MaxDiskUsagePercent > 100 {
		value.MaxDiskUsagePercent = 80
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	temporary := s.path + ".tmp"
	if err := os.WriteFile(temporary, data, 0600); err != nil {
		return err
	}
	if err := os.Rename(temporary, s.path); err != nil {
		return err
	}
	s.value = value
	return nil
}
func (s *NodeConfigStore) Get() NodeConfig { s.mu.RLock(); defer s.mu.RUnlock(); return s.value }

func collectMetrics(ctx context.Context, queue *LogQueue, configs *NodeConfigStore) {
	// Warm CPU sampler so the first real sample is meaningful.
	_, _ = cpu.Percent(0, false)
	metricsTicker := time.NewTicker(time.Minute)
	cacheTicker := time.NewTicker(10 * time.Second)
	defer metricsTicker.Stop()
	defer cacheTicker.Stop()
	enforceCacheLimit(configs.Get())
	for {
		select {
		case <-ctx.Done():
			return
		case <-cacheTicker.C:
			enforceCacheLimit(configs.Get())
		case <-metricsTicker.C:
			appendMetrics(queue, configs.Get())
		}
	}
}

func enforceCacheLimit(config NodeConfig) {
	_ = cachefs.Enforce(
		config.CacheDirectory,
		config.AutoMaxSize,
		config.MaxSizeBytes,
		config.MaxDiskUsagePercent,
	)
}

func appendMetrics(queue *LogQueue, config NodeConfig) {
	cpuValues, cpuErr := cpu.Percent(0, false)
	memory, _ := mem.VirtualMemory()
	loads, _ := load.Avg()
	connections, connectionErr := gnet.ConnectionsWithoutUids("tcp")
	usage, diskErr := disk.Usage(config.CacheDirectory)
	cacheUsed, cacheErr := cacheDirectorySize(config.CacheDirectory)
	payloadMap := map[string]any{
		"minute":             time.Now().UTC().Truncate(time.Minute),
		"cpu_usage_percent":  first(cpuValues),
		"memory_used_bytes":  memoryUsed(memory),
		"memory_total_bytes": memoryTotal(memory),
		"load_1":             loadAt(loads, 1),
		"load_5":             loadAt(loads, 5),
		"load_15":            loadAt(loads, 15),
		"connections":        establishedConnections(connections),
		"cache_directory":    config.CacheDirectory,
		"cache_used_bytes":   cacheUsed,
		"disk_used_bytes":    diskUsed(usage),
		"disk_total_bytes":   diskTotal(usage),
	}
	if cpuErr != nil {
		payloadMap["cpu_error"] = cpuErr.Error()
	}
	if diskErr != nil {
		payloadMap["disk_error"] = diskErr.Error()
		payloadMap["disk_used_bytes"] = nil
		payloadMap["disk_total_bytes"] = nil
	}
	if cacheErr != nil {
		payloadMap["cache_error"] = cacheErr.Error()
		payloadMap["cache_used_bytes"] = nil
	}
	if connectionErr != nil {
		payloadMap["connection_error"] = connectionErr.Error()
	}
	payload, _ := json.Marshal(payloadMap)
	_, _ = queue.Append(LogRecord{Type: "node_runtime", Payload: payload})
	appendOriginMetrics(queue)
}

func appendOriginMetrics(queue *LogQueue) {
	minute := time.Now().UTC().Truncate(time.Minute)
	for _, metric := range origingovernance.SnapshotAndReset() {
		appendOriginMetric(queue, minute, metric)
	}
}

func appendOriginMetric(queue *LogQueue, minute time.Time, metric origingovernance.Metric) {
	payload, err := json.Marshal(map[string]any{
		"minute": minute, "site_id": metric.SiteID, "origin_address": metric.OriginAddress,
		"healthy": metric.Healthy, "available": metric.Available, "fails": metric.Fails,
		"requests": metric.Requests, "errors": metric.Errors,
		"average_latency_ms": metric.AverageLatencyMS, "error_rate": metric.ErrorRate,
	})
	if err == nil {
		_, _ = queue.Append(LogRecord{Type: "origin_health", Payload: payload})
	}
}

func cacheDirectorySize(path string) (uint64, error) {
	var total uint64
	err := filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info != nil && info.Mode().IsRegular() {
			total += uint64(info.Size())
		}
		return nil
	})
	return total, err
}

func establishedConnections(connections []gnet.ConnectionStat) uint64 {
	var count uint64
	for _, connection := range connections {
		if connection.Status == "ESTABLISHED" {
			count++
		}
	}
	return count
}

func first(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	return values[0]
}
func memoryUsed(value *mem.VirtualMemoryStat) uint64 {
	if value == nil {
		return 0
	}
	return value.Used
}
func memoryTotal(value *mem.VirtualMemoryStat) uint64 {
	if value == nil {
		return 0
	}
	return value.Total
}
func loadAt(value *load.AvgStat, period int) float64 {
	if value == nil {
		return 0
	}
	if period == 1 {
		return value.Load1
	}
	if period == 5 {
		return value.Load5
	}
	return value.Load15
}
func diskUsed(value *disk.UsageStat) uint64 {
	if value == nil {
		return 0
	}
	return value.Used
}
func diskTotal(value *disk.UsageStat) uint64 {
	if value == nil {
		return 0
	}
	return value.Total
}
