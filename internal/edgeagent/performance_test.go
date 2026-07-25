package edgeagent

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	securitypolicy "goveto-edge/internal/policy"
)

func TestAgentPerformanceTarget(t *testing.T) {
	ensureAgentLogSink(t)
	var originRequests atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		originRequests.Add(1)
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(make([]byte, 16<<10))
	}))
	defer origin.Close()

	port := freePort(t)
	manager := NewConfigManager(filepath.Join(t.TempDir(), "sites.json"), ":"+strconv.Itoa(port))
	manager.nodeConfig = NodeConfig{
		CacheDirectory:      filepath.Join(t.TempDir(), "cache"),
		MaxSizeBytes:        128 << 20,
		MaxDiskUsagePercent: 95,
	}
	config := SiteConfig{
		SiteID:   "performance-site",
		Version:  1,
		Domains:  []string{"performance.example.test"},
		Listener: ListenerConfig{HTTPEnabled: true, HTTPPort: port},
		Origins:  []OriginConfig{{Protocol: "http", Address: strings.TrimPrefix(origin.URL, "http://")}},
		Cache:    enabledCachePolicy(t),
	}
	waf := securitypolicy.DefaultWAFPolicy()
	waf.Enabled = true
	for index := 0; index < 12; index++ {
		waf.Groups = append(waf.Groups, securitypolicy.WAFRuleGroup{
			ID: fmt.Sprintf("rule-%d", index), Enabled: true, Operator: "AND", Action: "BLOCK",
			Rules: []securitypolicy.WAFRequestRule{
				{Field: "PATH", Operator: "PREFIX", Value: fmt.Sprintf("/blocked-%d/", index)},
				{Field: "HEADER", Name: "X-Attack", Operator: "REGEX", Value: `(?i)(?:attack|scanner)`},
			},
		})
	}
	config.WAF = toMap(t, waf)
	if err := manager.ApplySite(config); err != nil {
		t.Fatal(err)
	}
	defer manager.Stop()

	transport := &http.Transport{
		MaxIdleConns:        128,
		MaxIdleConnsPerHost: 128,
		IdleConnTimeout:     30 * time.Second,
	}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	defer transport.CloseIdleConnections()
	request := func() (time.Duration, error) {
		req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+"/asset.bin", nil)
		if err != nil {
			return 0, err
		}
		req.Host = config.Domains[0]
		started := time.Now()
		response, err := client.Do(req)
		if err != nil {
			return 0, err
		}
		_, readErr := io.Copy(io.Discard, response.Body)
		closeErr := response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return 0, fmt.Errorf("unexpected status %d", response.StatusCode)
		}
		if readErr != nil {
			return 0, readErr
		}
		return time.Since(started), closeErr
	}

	if _, err := request(); err != nil {
		t.Fatalf("warm cache: %v", err)
	}
	const total = 5000
	const concurrency = 32
	latencies := make([]time.Duration, total)
	jobs := make(chan int, total)
	var failures atomic.Int64
	var workers sync.WaitGroup
	started := time.Now()
	for range concurrency {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				latency, err := request()
				if err != nil {
					failures.Add(1)
					continue
				}
				latencies[index] = latency
			}
		}()
	}
	for index := range total {
		jobs <- index
	}
	close(jobs)
	workers.Wait()
	elapsed := time.Since(started)

	if failures.Load() != 0 {
		t.Fatalf("pressure test had %d failed requests", failures.Load())
	}
	sort.Slice(latencies, func(left, right int) bool { return latencies[left] < latencies[right] })
	p95 := latencies[(total*95/100)-1]
	rps := float64(total) / elapsed.Seconds()
	t.Logf("cached edge throughput %.0f req/s, p95 %s, total %s", rps, p95, elapsed)
	if originRequests.Load() != 1 {
		t.Fatalf("cache pressure test reached origin %d times, want 1", originRequests.Load())
	}
	if rps < 2000 {
		t.Fatalf("cached edge throughput %.0f req/s is below 2000 req/s target", rps)
	}
	if p95 > 50*time.Millisecond {
		t.Fatalf("cached edge p95 %s exceeds 50ms target", p95)
	}
}
