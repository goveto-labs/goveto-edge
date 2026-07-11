package edgeagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"goveto-edge/internal/edgeprotocol"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
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
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			config := configs.Get()
			trimCache(config)
			appendMetrics(queue, config)
		}
	}
}

func cacheLimit(config NodeConfig) uint64 {
	if !config.AutoMaxSize {
		return config.MaxSizeBytes
	}
	usage, err := disk.Usage(config.CacheDirectory)
	if err != nil || usage == nil {
		return 0
	}
	return usage.Total * uint64(config.MaxDiskUsagePercent) / 100
}

func trimCache(config NodeConfig) {
	limit := cacheLimit(config)
	if limit == 0 {
		return
	}
	type cacheFile struct {
		path     string
		size     int64
		modified time.Time
	}
	files := make([]cacheFile, 0)
	var total uint64
	_ = filepath.Walk(config.CacheDirectory, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || !info.Mode().IsRegular() {
			return nil
		}
		total += uint64(info.Size())
		files = append(files, cacheFile{path: path, size: info.Size(), modified: info.ModTime()})
		return nil
	})
	if total <= limit {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].modified.Before(files[j].modified) })
	for _, file := range files {
		if total <= limit {
			break
		}
		if os.Remove(file.path) == nil {
			total -= uint64(file.size)
		}
	}
}

func appendMetrics(queue *LogQueue, config NodeConfig) {
	cpuValues, cpuErr := cpu.Percent(0, false)
	memory, _ := mem.VirtualMemory()
	loads, _ := load.Avg()
	usage, diskErr := disk.Usage(config.CacheDirectory)
	maxCache := config.MaxSizeBytes
	if config.AutoMaxSize && usage != nil && diskErr == nil {
		maxCache = usage.Total * uint64(config.MaxDiskUsagePercent) / 100
	}
	payloadMap := map[string]any{
		"minute":             time.Now().UTC().Truncate(time.Minute),
		"cpu_usage_percent":  first(cpuValues),
		"memory_used_bytes":  memoryUsed(memory),
		"memory_total_bytes": memoryTotal(memory),
		"load_1":             loadAt(loads, 1),
		"load_5":             loadAt(loads, 5),
		"load_15":            loadAt(loads, 15),
		"cache_directory":    config.CacheDirectory,
		"cache_used_bytes":   diskUsed(usage),
		"cache_max_bytes":    maxCache,
		"disk_used_bytes":    diskUsed(usage),
		"disk_total_bytes":   diskTotal(usage),
	}
	if cpuErr != nil {
		payloadMap["cpu_error"] = cpuErr.Error()
	}
	if diskErr != nil {
		payloadMap["disk_error"] = diskErr.Error()
		payloadMap["cache_used_bytes"] = nil
		payloadMap["disk_used_bytes"] = nil
		payloadMap["disk_total_bytes"] = nil
	}
	payload, _ := json.Marshal(payloadMap)
	_, _ = queue.Append(LogRecord{Type: "node_runtime", Payload: payload})
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
