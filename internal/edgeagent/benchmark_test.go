package edgeagent

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
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
