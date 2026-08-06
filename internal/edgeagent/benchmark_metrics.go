package edgeagent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	httppprof "net/http/pprof"
	"runtime"
	"runtime/debug"
	"sync"
	"time"

	"goveto-edge/caddy/agentlog"
	"goveto-edge/caddy/origingovernance"
	cachefs "goveto-edge/caddy/simplefs"
)

func serveBenchmarkMetrics(ctx context.Context, address string, queue *LogQueue, configs *NodeConfigStore) {
	runtime.SetMutexProfileFraction(5)
	server := &http.Server{Addr: address, Handler: benchmarkMetricsHandler(queue, configs), ReadHeaderTimeout: 2 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownContext)
	}()
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) && ctx.Err() == nil {
		slog.Error("serve benchmark metrics", "error", err)
	}
}

func benchmarkMetricsHandler(queue *LogQueue, configs *NodeConfigStore) http.Handler {
	mux := http.NewServeMux()
	var gcMu sync.Mutex
	mux.HandleFunc("GET /metrics", func(response http.ResponseWriter, _ *http.Request) {
		var memory runtime.MemStats
		runtime.ReadMemStats(&memory)
		var queueStats LogQueueStats
		var queueErr error
		if queue != nil {
			queueStats, queueErr = queue.Stats()
		}
		cacheDirectory := ""
		if configs != nil {
			cacheDirectory = configs.Get().CacheDirectory
		}
		cacheStats := cachefs.Stats(cacheDirectory)
		payload := map[string]any{
			"heap_bytes": memory.HeapAlloc, "total_alloc_bytes": memory.TotalAlloc,
			"heap_inuse_bytes": memory.HeapInuse, "heap_idle_bytes": memory.HeapIdle,
			"heap_released_bytes": memory.HeapReleased,
			"gc_count":            memory.NumGC, "goroutines": runtime.NumGoroutine(),
			"cache_hits": cacheStats.Hits, "cache_misses": cacheStats.Misses,
			"cache_evictions":                cacheStats.Evictions,
			"cache_body_entries":             cacheStats.BodyEntries,
			"cache_mapping_entries":          cacheStats.MappingEntries,
			"cache_expiration_entries":       cacheStats.ExpirationEntries,
			"cache_accounted_bytes":          cacheStats.AccountedBytes,
			"cache_physical_bytes":           cacheStats.PhysicalBytes,
			"cache_index_bytes":              cacheStats.IndexBytes,
			"cache_index_free_pages":         cacheStats.IndexFreePages,
			"cache_index_pending_pages":      cacheStats.IndexPendingPages,
			"cache_write_queue_depth":        cacheStats.WriteQueueDepth,
			"cache_write_queue_bytes":        cacheStats.WriteQueueBytes,
			"cache_write_queue_depth_max":    cacheStats.WriteQueueDepthMax,
			"cache_write_queue_bytes_max":    cacheStats.WriteQueueBytesMax,
			"cache_write_queue_rejections":   cacheStats.WriteQueueRejections,
			"cache_write_batches":            cacheStats.WriteBatches,
			"cache_write_objects_committed":  cacheStats.WriteObjectsCommitted,
			"cache_average_write_batch_size": cacheStats.AverageWriteBatchSize,
			"cache_write_commit_latency_ms":  cacheStats.WriteCommitLatencyMS,
			"cache_inflight_writes":          cacheStats.InflightWrites,
		}
		if queue != nil && queueErr == nil {
			payload["log_queue_bytes"] = queueStats.Bytes
			payload["log_queue_records"] = queueStats.Records
			payload["log_buffer_bytes"] = queueStats.MemoryBufferBytes
			payload["log_buffer_records"] = queueStats.MemoryBufferRecords
			payload["dropped_logs"] = queueStats.DroppedRecords
			payload["memory_dropped_logs"] = queueStats.MemoryDroppedRecords
			payload["disk_dropped_logs"] = queueStats.DiskDroppedRecords
			payload["committed_log_batches"] = queueStats.CommittedBatches
			payload["committed_log_records"] = queueStats.CommittedRecords
			payload["average_log_batch_size"] = queueStats.AverageBatchSize
			payload["last_log_persist_error"] = queueStats.LastPersistError
			if !queueStats.LastPersistSuccess.IsZero() {
				payload["last_log_persist_success"] = queueStats.LastPersistSuccess
			}
		} else if queueErr != nil {
			payload["queue_error"] = queueErr.Error()
		}
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(payload)
	})
	mux.HandleFunc("POST /cache/drain", func(response http.ResponseWriter, _ *http.Request) {
		if configs == nil {
			http.Error(response, "cache configuration is unavailable", http.StatusServiceUnavailable)
			return
		}
		cachefs.Drain(configs.Get().CacheDirectory)
		response.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /cache/reset", func(response http.ResponseWriter, _ *http.Request) {
		if configs == nil {
			http.Error(response, "cache configuration is unavailable", http.StatusServiceUnavailable)
			return
		}
		if err := cachefs.ResetPath(configs.Get().CacheDirectory); err != nil {
			http.Error(response, err.Error(), http.StatusInternalServerError)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /gc", func(response http.ResponseWriter, _ *http.Request) {
		gcMu.Lock()
		debug.FreeOSMemory()
		gcMu.Unlock()
		response.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /variant", func(response http.ResponseWriter, _ *http.Request) {
		variant := "control"
		if queue != nil && queue.benchmarkAccessLogs.Load() {
			variant = "full"
		}
		_ = json.NewEncoder(response).Encode(map[string]string{"variant": variant})
	})
	mux.HandleFunc("POST /variant", func(response http.ResponseWriter, request *http.Request) {
		if queue == nil {
			http.Error(response, "log queue is unavailable", http.StatusServiceUnavailable)
			return
		}
		variant := request.URL.Query().Get("value")
		if variant != "full" && variant != "control" {
			http.Error(response, "variant must be full or control", http.StatusBadRequest)
			return
		}
		enabled := variant == "full"
		queue.setBenchmarkAccessLogsEnabled(enabled)
		agentlog.SetBenchmarkAccessLogsEnabled(enabled)
		origingovernance.SetBenchmarkObservabilityEnabled(enabled)
		response.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /debug/pprof/profile", httppprof.Profile)
	mux.Handle("GET /debug/pprof/mutex", httppprof.Handler("mutex"))
	mux.Handle("GET /debug/pprof/allocs", httppprof.Handler("allocs"))
	mux.Handle("GET /debug/pprof/heap", httppprof.Handler("heap"))
	mux.Handle("GET /debug/pprof/goroutine", httppprof.Handler("goroutine"))
	return mux
}
