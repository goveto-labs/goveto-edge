package edgeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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

var cacheAlertSamples = struct {
	sync.Mutex
	values map[string]cachefs.Statistics
}{values: map[string]cachefs.Statistics{}}

func NewNodeConfigStore(path string) *NodeConfigStore {
	store := &NodeConfigStore{path: path, value: NodeConfig{CacheDirectory: "/opt/goveto-edge/cache", AutoMaxSize: true, MaxDiskUsagePercent: 80}}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &store.value)
	}
	if store.value.CacheDirectory == "" {
		store.value.CacheDirectory = "/opt/goveto-edge/cache"
	}
	store.value.MaxDiskUsagePercent = normalizeCacheDiskPercent(store.value.MaxDiskUsagePercent)
	return store
}
func (s *NodeConfigStore) Set(value NodeConfig) error {
	if value.CacheDirectory == "" {
		value.CacheDirectory = "/opt/goveto-edge/cache"
	}
	value.MaxDiskUsagePercent = normalizeCacheDiskPercent(value.MaxDiskUsagePercent)
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
	initialSample := time.NewTimer(time.Second)
	metricsTicker := time.NewTicker(time.Minute)
	cacheTicker := time.NewTicker(10 * time.Second)
	defer initialSample.Stop()
	defer metricsTicker.Stop()
	defer cacheTicker.Stop()
	enforceCacheLimit(configs.Get())
	for {
		select {
		case <-ctx.Done():
			return
		case <-cacheTicker.C:
			enforceCacheLimit(configs.Get())
		case <-initialSample.C:
			if err := appendMetrics(queue, configs.Get()); err != nil {
				slog.Warn("collect initial node runtime metrics", "error", err)
			}
		case <-metricsTicker.C:
			if err := appendMetrics(queue, configs.Get()); err != nil {
				slog.Warn("collect node runtime metrics", "error", err)
			}
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

func appendMetrics(queue *LogQueue, config NodeConfig) error {
	cpuValues, cpuErr := cpu.Percent(0, false)
	memory, _ := mem.VirtualMemory()
	loads, _ := load.Avg()
	connections, connectionErr := gnet.ConnectionsWithoutUids("tcp")
	usage, diskErr := disk.Usage(config.CacheDirectory)
	cacheUsed, cacheErr := cacheDirectorySize(config.CacheDirectory)
	cacheStats := cachefs.Stats(config.CacheDirectory)
	cacheActivity := cacheActivitySinceLast(config.CacheDirectory, cacheStats)
	capacityRatio := cacheCapacityRatio(config, cacheUsed, usage)
	payloadMap := map[string]any{
		"minute":                time.Now().UTC().Truncate(time.Minute),
		"cpu_usage_percent":     first(cpuValues),
		"memory_used_bytes":     memoryUsed(memory),
		"memory_total_bytes":    memoryTotal(memory),
		"load_1":                loadAt(loads, 1),
		"load_5":                loadAt(loads, 5),
		"load_15":               loadAt(loads, 15),
		"connections":           establishedConnections(connections),
		"cache_directory":       config.CacheDirectory,
		"cache_used_bytes":      cacheUsed,
		"cache_entries":         cacheStats.Entries,
		"cache_hits":            cacheStats.Hits,
		"cache_misses":          cacheStats.Misses,
		"cache_stale_hits":      cacheStats.StaleHits,
		"cache_evictions":       cacheStats.Evictions,
		"cache_rejected_writes": cacheStats.RejectedWrites,
		"cache_corruptions":     cacheStats.Corruptions,
		"cache_hit_rate":        cacheStats.HitRate,
		"cache_capacity_ratio":  capacityRatio,
		"cache_alerts":          cacheAlerts(cacheActivity, capacityRatio),
		"disk_used_bytes":       diskUsed(usage),
		"disk_total_bytes":      diskTotal(usage),
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
	payload, err := json.Marshal(payloadMap)
	if err != nil {
		return fmt.Errorf("encode node runtime metrics: %w", err)
	}
	if _, err := queue.Append(LogRecord{Type: "node_runtime", Payload: payload}); err != nil {
		return fmt.Errorf("queue node runtime metrics: %w", err)
	}
	appendOriginMetrics(queue)
	return nil
}

func cacheActivitySinceLast(path string, current cachefs.Statistics) cachefs.Statistics {
	cacheAlertSamples.Lock()
	previous := cacheAlertSamples.values[path]
	cacheAlertSamples.values[path] = current
	cacheAlertSamples.Unlock()

	activity := cachefs.Statistics{
		Hits:           counterDelta(current.Hits, previous.Hits),
		Misses:         counterDelta(current.Misses, previous.Misses),
		StaleHits:      counterDelta(current.StaleHits, previous.StaleHits),
		Evictions:      counterDelta(current.Evictions, previous.Evictions),
		RejectedWrites: counterDelta(current.RejectedWrites, previous.RejectedWrites),
		Corruptions:    counterDelta(current.Corruptions, previous.Corruptions),
	}
	if total := activity.Hits + activity.Misses; total > 0 {
		activity.HitRate = float64(activity.Hits) / float64(total)
	}
	return activity
}

func counterDelta(current, previous uint64) uint64 {
	if current < previous {
		return current
	}
	return current - previous
}

func cacheCapacityRatio(config NodeConfig, cacheUsed uint64, usage *disk.UsageStat) float64 {
	diskRatio := 0.0
	if usage != nil && usage.Total > 0 {
		percent := normalizeCacheDiskPercent(config.MaxDiskUsagePercent)
		target := float64(usage.Total) * float64(percent) / 100
		diskRatio = float64(usage.Used) / target
	}
	if !config.AutoMaxSize && config.MaxSizeBytes > 0 {
		return max(float64(cacheUsed)/float64(config.MaxSizeBytes), diskRatio)
	}
	return diskRatio
}

func normalizeCacheDiskPercent(value int) int {
	if value < 1 {
		return 80
	}
	if value > 90 {
		return 90
	}
	return value
}

func cacheAlerts(stats cachefs.Statistics, capacityRatio float64) []string {
	alerts := make([]string, 0, 4)
	if capacityRatio >= 0.9 {
		alerts = append(alerts, "CAPACITY_HIGH")
	}
	if stats.Evictions > 0 {
		alerts = append(alerts, "EVICTIONS")
	}
	if stats.RejectedWrites > 0 {
		alerts = append(alerts, "WRITE_REJECTED")
	}
	if stats.Corruptions > 0 {
		alerts = append(alerts, "CORRUPTION_RECOVERED")
	}
	if stats.Hits+stats.Misses >= 100 && stats.HitRate < 0.5 {
		alerts = append(alerts, "HIT_RATE_LOW")
	}
	return alerts
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
