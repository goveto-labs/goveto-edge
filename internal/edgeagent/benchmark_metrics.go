package edgeagent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"runtime"
	"time"

	cachefs "goveto-edge/caddy/simplefs"
)

func serveBenchmarkMetrics(ctx context.Context, address string, queue *LogQueue, configs *NodeConfigStore) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(response http.ResponseWriter, _ *http.Request) {
		var memory runtime.MemStats
		runtime.ReadMemStats(&memory)
		queueStats, queueErr := queue.Stats()
		cacheStats := cachefs.Stats(configs.Get().CacheDirectory)
		payload := map[string]any{
			"heap_bytes": memory.HeapAlloc, "total_alloc_bytes": memory.TotalAlloc,
			"gc_count": memory.NumGC, "goroutines": runtime.NumGoroutine(),
			"cache_hits": cacheStats.Hits, "cache_misses": cacheStats.Misses,
			"cache_evictions": cacheStats.Evictions,
		}
		if queueErr == nil {
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
		} else {
			payload["queue_error"] = queueErr.Error()
		}
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(payload)
	})
	server := &http.Server{Addr: address, Handler: mux, ReadHeaderTimeout: 2 * time.Second}
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
