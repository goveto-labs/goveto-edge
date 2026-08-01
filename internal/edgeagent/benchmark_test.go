package edgeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func BenchmarkRenderCaddyConfig(b *testing.B) {
	for _, siteCount := range []int{1, 100, 1000} {
		b.Run(fmt.Sprintf("sites_%d", siteCount), func(b *testing.B) {
			sites := make(map[string]SiteConfig, siteCount)
			for index := range siteCount {
				id := fmt.Sprintf("site-%04d", index)
				sites[id] = SiteConfig{
					SiteID: id, Version: 1, Domains: []string{fmt.Sprintf("%s.example.test", id)},
					Listener: ListenerConfig{HTTPEnabled: true, HTTPPort: 8080},
					Origins:  []OriginConfig{{Protocol: "http", Address: "origin:8080"}},
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, err := renderCaddyConfig(sites, ":8080", ""); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkLogQueue(b *testing.B) {
	payload, _ := json.Marshal(map[string]any{"status": 200, "path": "/assets/app.js", "padding": "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"})
	b.Run("Append", func(b *testing.B) {
		queue, err := OpenLogQueue(filepath.Join(b.TempDir(), "append.db"), 1<<40)
		if err != nil {
			b.Fatal(err)
		}
		defer queue.Close()
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			if _, err := queue.Append(LogRecord{Type: "access", Payload: payload}); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("AppendBatch512", func(b *testing.B) {
		queue, err := OpenLogQueue(filepath.Join(b.TempDir(), "append-batch.db"), 1<<40)
		if err != nil {
			b.Fatal(err)
		}
		defer queue.Close()
		records := make([]LogRecord, 512)
		for index := range records {
			records[index] = LogRecord{Type: "access", Payload: payload}
		}
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			if _, err := queue.AppendBatch(records); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("EnqueueAccess", func(b *testing.B) {
		queue, err := OpenLogQueue(filepath.Join(b.TempDir(), "enqueue.db"), 1<<40)
		if err != nil {
			b.Fatal(err)
		}
		if err := queue.StartAccessPipeline(LogPolicy{SampleRate: 0}, defaultAccessLogConfig()); err != nil {
			b.Fatal(err)
		}
		b.ReportAllocs()
		b.ResetTimer()
		b.RunParallel(func(worker *testing.PB) {
			for worker.Next() {
				queue.EnqueueAccess(LogRecord{Type: "access", Payload: payload})
			}
		})
		b.StopTimer()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := queue.ShutdownAccess(shutdownContext); err != nil {
			b.Fatal(err)
		}
		if err := queue.Close(); err != nil {
			b.Fatal(err)
		}
	})
	b.Run("AccessPipeline", func(b *testing.B) {
		queue, err := OpenLogQueue(filepath.Join(b.TempDir(), "pipeline.db"), 1<<40)
		if err != nil {
			b.Fatal(err)
		}
		policy := LogPolicy{SampleRate: 1, RedactQuery: true, AnonymizeIP: true, RedactedHeaders: map[string]struct{}{"authorization": {}}}
		if err := queue.StartAccessPipeline(policy, defaultAccessLogConfig()); err != nil {
			b.Fatal(err)
		}
		accessPayload := json.RawMessage(`{"request":{"uri":"/asset?token=secret","client_ip":"192.0.2.129","headers":{"Authorization":["secret"]}},"status":200}`)
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			for !queue.EnqueueSanitizedAccess(LogRecord{Type: "access", Payload: accessPayload}) {
				runtime.Gosched()
			}
		}
		b.StopTimer()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := queue.ShutdownAccess(shutdownContext); err != nil {
			b.Fatal(err)
		}
		stats, err := queue.Stats()
		if err != nil {
			b.Fatal(err)
		}
		b.ReportMetric(stats.AverageBatchSize, "records/batch")
		b.ReportMetric(float64(stats.MemoryDroppedRecords), "dropped")
		if stats.CommittedRecords != uint64(b.N) || stats.MemoryDroppedRecords != 0 {
			b.Fatalf("submitted=%d committed=%d dropped=%d", b.N, stats.CommittedRecords, stats.MemoryDroppedRecords)
		}
		if err := queue.Close(); err != nil {
			b.Fatal(err)
		}
	})
	b.Run("FullAccessBufferReject", func(b *testing.B) {
		queue, err := OpenLogQueue(filepath.Join(b.TempDir(), "full-buffer.db"), 1<<40)
		if err != nil {
			b.Fatal(err)
		}
		if err = queue.StartAccessPipeline(LogPolicy{SampleRate: 1}, AccessLogConfig{
			BufferBytes: 1024, BufferRecords: 1, BatchBytes: 1 << 20, BatchRecords: 1024, FlushInterval: time.Hour,
		}); err != nil {
			b.Fatal(err)
		}
		if !queue.EnqueueSanitizedAccess(LogRecord{Type: "access", Payload: payload}) {
			b.Fatal("failed to fill access buffer")
		}
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			if queue.EnqueueSanitizedAccess(LogRecord{Type: "access", Payload: payload}) {
				b.Fatal("full buffer accepted a record")
			}
		}
		b.StopTimer()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err = queue.ShutdownAccess(shutdownContext); err != nil {
			b.Fatal(err)
		}
		if err = queue.Close(); err != nil {
			b.Fatal(err)
		}
	})
	b.Run("Batch1000", func(b *testing.B) {
		queue, err := OpenLogQueue(filepath.Join(b.TempDir(), "batch.db"), 1<<30)
		if err != nil {
			b.Fatal(err)
		}
		defer queue.Close()
		for range 1000 {
			if _, err := queue.Append(LogRecord{Type: "access", Payload: payload}); err != nil {
				b.Fatal(err)
			}
		}
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			if _, err := queue.Batch(1000); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("Ack", func(b *testing.B) {
		queue, err := OpenLogQueue(filepath.Join(b.TempDir(), "ack.db"), 1<<40)
		if err != nil {
			b.Fatal(err)
		}
		defer queue.Close()
		ids := make([]uint64, b.N)
		for index := range b.N {
			ids[index], err = queue.Append(LogRecord{Type: "access", Payload: payload})
			if err != nil {
				b.Fatal(err)
			}
		}
		b.ReportAllocs()
		b.ResetTimer()
		for _, id := range ids {
			if err := queue.Ack(id); err != nil {
				b.Fatal(err)
			}
		}
	})
}
