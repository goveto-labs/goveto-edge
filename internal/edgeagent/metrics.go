package edgeagent

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
)

type NodeConfig struct {
	CacheDirectory      string `json:"cache_directory"`
	AutoMaxSize         bool   `json:"auto_max_size"`
	MaxSizeBytes        uint64 `json:"max_size_bytes"`
	MaxDiskUsagePercent int    `json:"max_disk_usage_percent"`
}

type NodeConfigStore struct {
	mu    sync.RWMutex
	value NodeConfig
}

func NewNodeConfigStore() *NodeConfigStore {
	return &NodeConfigStore{value: NodeConfig{CacheDirectory: "/opt/goveto-edge/cache", AutoMaxSize: true, MaxDiskUsagePercent: 80}}
}
func (s *NodeConfigStore) Set(value NodeConfig) {
	if value.CacheDirectory == "" {
		value.CacheDirectory = "/opt/goveto-edge/cache"
	}
	if value.MaxDiskUsagePercent < 1 || value.MaxDiskUsagePercent > 100 {
		value.MaxDiskUsagePercent = 80
	}
	s.mu.Lock()
	s.value = value
	s.mu.Unlock()
}
func (s *NodeConfigStore) Get() NodeConfig { s.mu.RLock(); defer s.mu.RUnlock(); return s.value }

func collectMetrics(ctx context.Context, queue *LogQueue, configs *NodeConfigStore) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			appendMetrics(queue, configs.Get())
		}
	}
}

func appendMetrics(queue *LogQueue, config NodeConfig) {
	cpuValues, _ := cpu.Percent(0, false)
	memory, _ := mem.VirtualMemory()
	loads, _ := load.Avg()
	usage, _ := disk.Usage(config.CacheDirectory)
	maxCache := config.MaxSizeBytes
	if config.AutoMaxSize && usage != nil {
		maxCache = usage.Total * uint64(config.MaxDiskUsagePercent) / 100
	}
	payload, _ := json.Marshal(map[string]any{"minute": time.Now().UTC().Truncate(time.Minute), "cpu_usage_percent": first(cpuValues), "memory_used_bytes": memoryUsed(memory), "memory_total_bytes": memoryTotal(memory), "load_1": loadAt(loads, 1), "load_5": loadAt(loads, 5), "load_15": loadAt(loads, 15), "cache_directory": config.CacheDirectory, "cache_used_bytes": diskUsed(usage), "cache_max_bytes": maxCache, "disk_used_bytes": diskUsed(usage), "disk_total_bytes": diskTotal(usage)})
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
