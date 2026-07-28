package agentbench

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/http/httptrace"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go/http3"
	"github.com/shirou/gopsutil/v4/process"
	"golang.org/x/net/http2"
)

type closeRoundTripper interface {
	http.RoundTripper
	Close() error
}

type runState struct {
	requests   atomic.Uint64
	successes  atomic.Uint64
	failures   atomic.Uint64
	bytes      atomic.Uint64
	mu         sync.Mutex
	latencies  []time.Duration
	handshakes []time.Duration
	ttfb       []time.Duration
	protocols  map[string]uint64
	errors     []string
}

func RunBenchmark(ctx context.Context, config Config) (Report, error) {
	if err := validateConfig(&config); err != nil {
		return Report{}, err
	}
	report := Report{
		SchemaVersion: SchemaVersion,
		GeneratedAt:   time.Now().UTC(),
		Platform:      CollectPlatform(),
		Scenario: Scenario{
			Suite: config.Suite, Name: config.Scenario, Protocol: config.Protocol, URL: config.URL,
			Concurrency: config.Concurrency, DurationMS: config.Duration.Milliseconds(), WarmupMS: config.Warmup.Milliseconds(),
			Repeats: config.Repeats, NewConnection: config.NewConnection, ExpectedStatus: config.ExpectedStatus,
			ExpectedSHA256: config.ExpectedSHA256, ExpectedHeaders: config.ExpectedHeaders,
		},
	}
	var loadCPUMax float64
	for index := 1; index <= config.Repeats; index++ {
		run, runLoadCPU, err := runOnce(ctx, config, index)
		report.Runs = append(report.Runs, run)
		loadCPUMax = max(loadCPUMax, runLoadCPU)
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			return report, err
		}
		if ctx.Err() != nil {
			return report, ctx.Err()
		}
	}
	report.Summary = Summarize(report.Runs)
	report.Validity = ValidateRuns(report.Runs, loadCPUMax)
	return report, nil
}

func runOnce(ctx context.Context, config Config, index int) (Run, float64, error) {
	transport, closeTransport, err := newTransport(config)
	if err != nil {
		return Run{}, 0, err
	}
	defer closeTransport()
	client := &http.Client{Transport: transport, Timeout: config.RequestTimeout}

	if config.Warmup > 0 {
		warmContext, cancel := context.WithTimeout(ctx, config.Warmup)
		runWorkers(warmContext, config, client, nil)
		cancel()
	}

	state := &runState{protocols: make(map[string]uint64)}
	started := time.Now().UTC()
	measureContext, cancel := context.WithTimeout(ctx, config.Duration)
	defer cancel()
	samplesDone := make(chan struct{})
	var samples []TimeSeriesPoint
	var resources ResourceSummary
	var loadCPUMax float64
	go func() {
		defer close(samplesDone)
		samples, resources, loadCPUMax = sampleResources(measureContext, config.AgentPID, config.AgentMetricsURL, config.SampleInterval, state)
	}()
	runWorkers(measureContext, config, client, state)
	<-samplesDone
	elapsed := time.Since(started)
	metrics, failures := state.metrics(elapsed)
	return Run{Index: index, StartedAt: started, Metrics: metrics, Resources: resources, Samples: samples, Errors: failures}, loadCPUMax, nil
}

func runWorkers(ctx context.Context, config Config, sharedClient *http.Client, state *runState) {
	var workers sync.WaitGroup
	for range config.Concurrency {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for ctx.Err() == nil {
				client := sharedClient
				closeClient := func() {}
				if config.NewConnection {
					transport, closeTransport, err := newTransport(config)
					if err != nil {
						if state != nil {
							state.recordFailure(err)
						}
						return
					}
					client = &http.Client{Transport: transport, Timeout: config.RequestTimeout}
					closeClient = closeTransport
				}
				result := executeRequest(ctx, client, config)
				closeClient()
				if state != nil && !(ctx.Err() != nil && (errors.Is(result.err, context.DeadlineExceeded) || errors.Is(result.err, context.Canceled))) {
					state.record(result)
				}
			}
		}()
	}
	workers.Wait()
}

type requestResult struct {
	latency   time.Duration
	handshake time.Duration
	ttfb      time.Duration
	bytes     uint64
	protocol  string
	err       error
}

func executeRequest(ctx context.Context, client *http.Client, config Config) requestResult {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, config.URL, nil)
	if err != nil {
		return requestResult{err: err}
	}
	if config.Host != "" {
		request.Host = config.Host
	}
	request.Header.Set("Accept-Encoding", "identity")
	var tlsStarted, wroteRequest time.Time
	var handshake, ttfb time.Duration
	trace := &httptrace.ClientTrace{
		TLSHandshakeStart: func() { tlsStarted = time.Now() },
		TLSHandshakeDone: func(tls.ConnectionState, error) {
			if !tlsStarted.IsZero() {
				handshake = time.Since(tlsStarted)
			}
		},
		WroteRequest: func(httptrace.WroteRequestInfo) { wroteRequest = time.Now() },
		GotFirstResponseByte: func() {
			if !wroteRequest.IsZero() {
				ttfb = time.Since(wroteRequest)
			}
		},
	}
	request = request.WithContext(httptrace.WithClientTrace(request.Context(), trace))
	started := time.Now()
	response, err := client.Do(request)
	if err != nil {
		return requestResult{latency: time.Since(started), handshake: handshake, ttfb: ttfb, err: err}
	}
	defer response.Body.Close()
	var destination io.Writer = io.Discard
	var digest hash.Hash
	if config.ExpectedSHA256 != "" {
		digest = sha256.New()
		destination = digest
	}
	written, readErr := io.Copy(destination, response.Body)
	result := requestResult{latency: time.Since(started), handshake: handshake, ttfb: ttfb, bytes: uint64(max(written, 0)), protocol: response.Proto}
	if readErr != nil {
		result.err = fmt.Errorf("read response: %w", readErr)
		return result
	}
	if response.StatusCode != config.ExpectedStatus {
		result.err = fmt.Errorf("status %d, want %d", response.StatusCode, config.ExpectedStatus)
		return result
	}
	if expected := protocolName(config.Protocol); response.Proto != expected {
		result.err = fmt.Errorf("negotiated %s, want %s", response.Proto, expected)
		return result
	}
	if digest != nil && !strings.EqualFold(hex.EncodeToString(digest.Sum(nil)), config.ExpectedSHA256) {
		result.err = fmt.Errorf("response SHA-256 mismatch")
		return result
	}
	for name, expected := range config.ExpectedHeaders {
		if actual := response.Header.Get(name); actual != expected {
			result.err = fmt.Errorf("header %s=%q, want %q", name, actual, expected)
			return result
		}
	}
	return result
}

func newTransport(config Config) (http.RoundTripper, func(), error) {
	tlsConfig := &tls.Config{InsecureSkipVerify: config.InsecureSkipVerify, ServerName: serverName(config)} // benchmark targets may use private test certificates.
	switch config.Protocol {
	case ProtocolH1:
		transport := &http.Transport{TLSClientConfig: tlsConfig, ForceAttemptHTTP2: false, MaxIdleConns: config.Concurrency * 2, MaxIdleConnsPerHost: config.Concurrency * 2, DisableCompression: true}
		return transport, transport.CloseIdleConnections, nil
	case ProtocolH2:
		transport := &http2.Transport{TLSClientConfig: tlsConfig, DisableCompression: true, StrictMaxConcurrentStreams: true}
		return transport, func() { transport.CloseIdleConnections() }, nil
	case ProtocolH3:
		transport := &http3.Transport{TLSClientConfig: tlsConfig, DisableCompression: true}
		return transport, func() { _ = transport.Close() }, nil
	default:
		return nil, func() {}, fmt.Errorf("unsupported protocol %q", config.Protocol)
	}
}

func validateConfig(config *Config) error {
	if config.URL == "" {
		return errors.New("URL is required")
	}
	if config.Scenario == "" {
		return errors.New("scenario is required")
	}
	if config.Concurrency <= 0 {
		return errors.New("concurrency must be positive")
	}
	if config.Duration <= 0 {
		return errors.New("duration must be positive")
	}
	if config.Repeats <= 0 {
		config.Repeats = 1
	}
	if config.RequestTimeout <= 0 {
		config.RequestTimeout = 10 * time.Second
	}
	if config.SampleInterval <= 0 {
		config.SampleInterval = time.Second
	}
	if config.ExpectedStatus == 0 {
		config.ExpectedStatus = http.StatusOK
	}
	if config.ExpectedHeaders == nil {
		config.ExpectedHeaders = map[string]string{}
	}
	if config.Suite == "" {
		config.Suite = SuitePR
	}
	if config.Protocol != ProtocolH1 && config.Protocol != ProtocolH2 && config.Protocol != ProtocolH3 {
		return fmt.Errorf("invalid protocol %q", config.Protocol)
	}
	return nil
}

func protocolName(protocol Protocol) string {
	switch protocol {
	case ProtocolH1:
		return "HTTP/1.1"
	case ProtocolH2:
		return "HTTP/2.0"
	case ProtocolH3:
		return "HTTP/3.0"
	}
	return ""
}

func serverName(config Config) string {
	if config.Host != "" {
		return strings.Split(config.Host, ":")[0]
	}
	return ""
}

func (state *runState) record(result requestResult) {
	state.requests.Add(1)
	state.bytes.Add(result.bytes)
	if result.err != nil {
		state.recordFailure(result.err)
		return
	}
	state.successes.Add(1)
	state.mu.Lock()
	state.latencies = append(state.latencies, result.latency)
	if result.handshake > 0 {
		state.handshakes = append(state.handshakes, result.handshake)
	}
	if result.ttfb > 0 {
		state.ttfb = append(state.ttfb, result.ttfb)
	}
	state.protocols[result.protocol]++
	state.mu.Unlock()
}

func (state *runState) recordFailure(err error) {
	state.failures.Add(1)
	state.mu.Lock()
	if len(state.errors) < 20 {
		state.errors = append(state.errors, err.Error())
	}
	state.mu.Unlock()
}

func (state *runState) metrics(elapsed time.Duration) (Metrics, []string) {
	state.mu.Lock()
	defer state.mu.Unlock()
	seconds := elapsed.Seconds()
	requests, successes, failures := state.requests.Load(), state.successes.Load(), state.failures.Load()
	result := Metrics{Requests: requests, Successes: successes, Failures: failures, Bytes: state.bytes.Load()}
	if seconds > 0 {
		result.RPS = float64(requests) / seconds
		result.BytesPerSecond = float64(result.Bytes) / seconds
	}
	if requests > 0 {
		result.SuccessRate = float64(successes) / float64(requests)
	}
	result.P50MS = durationMS(Percentile(state.latencies, 50))
	result.P95MS = durationMS(Percentile(state.latencies, 95))
	result.P99MS = durationMS(Percentile(state.latencies, 99))
	result.MaxMS = durationMS(Percentile(state.latencies, 100))
	result.TLSHandshakeMS = durationMS(Percentile(state.handshakes, 50))
	result.TTFBMS = durationMS(Percentile(state.ttfb, 50))
	var protocols []string
	for protocol := range state.protocols {
		protocols = append(protocols, protocol)
	}
	sort.Strings(protocols)
	result.NegotiatedProtocol = strings.Join(protocols, ",")
	return result, append([]string(nil), state.errors...)
}

func durationMS(value time.Duration) float64 { return float64(value) / float64(time.Millisecond) }

func sampleResources(ctx context.Context, pid int32, metricsURL string, interval time.Duration, state *runState) ([]TimeSeriesPoint, ResourceSummary, float64) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	loadProcess, _ := process.NewProcess(int32(os.Getpid()))
	if loadProcess != nil {
		_, _ = loadProcess.CPUPercent()
	}
	var target *process.Process
	if pid > 0 {
		target, _ = process.NewProcess(pid)
	}
	var points []TimeSeriesPoint
	var summary ResourceSummary
	var loadCPUMax float64
	var previousRequests, previousTotalAlloc uint64
	metricsClient := &http.Client{Timeout: min(interval, 2*time.Second)}
	for {
		select {
		case <-ctx.Done():
			return points, summary, loadCPUMax
		case at := <-ticker.C:
			requests, failures := state.requests.Load(), state.failures.Load()
			point := TimeSeriesPoint{At: at.UTC(), Requests: requests, Failures: failures, RPS: float64(requests-previousRequests) / interval.Seconds()}
			previousRequests = requests
			if loadProcess != nil {
				if value, err := loadProcess.CPUPercent(); err == nil {
					loadCPUMax = max(loadCPUMax, value/float64(max(runtime.NumCPU(), 1)))
				}
			}
			if target != nil {
				point.CPUPercent, _ = target.CPUPercent()
				if memory, err := target.MemoryInfo(); err == nil {
					point.RSSBytes = memory.RSS
				}
				point.FDs, _ = target.NumFDs()
				if connections, err := target.Connections(); err == nil {
					point.Connections = len(connections)
				}
				if counters, err := target.IOCounters(); err == nil {
					summary.ReadBytes = counters.ReadBytes
					summary.WriteBytes = counters.WriteBytes
				}
				summary.CPUPercentMax = max(summary.CPUPercentMax, point.CPUPercent)
				summary.RSSBytesMax = max(summary.RSSBytesMax, point.RSSBytes)
				summary.FDsMax = max(summary.FDsMax, point.FDs)
				summary.ConnectionsMax = max(summary.ConnectionsMax, point.Connections)
			}
			if metricsURL != "" {
				if telemetry, err := fetchTelemetry(ctx, metricsClient, metricsURL); err == nil {
					point.HeapBytes = telemetry.HeapBytes
					point.GCCount = telemetry.GCCount
					point.Goroutines = telemetry.Goroutines
					point.QueueBytes = telemetry.QueueBytes
					point.QueueRecords = telemetry.QueueRecords
					point.DroppedLogs = telemetry.DroppedLogs
					point.CacheHits = telemetry.CacheHits
					point.CacheMisses = telemetry.CacheMisses
					point.CacheEvictions = telemetry.CacheEvictions
					if previousTotalAlloc > 0 && telemetry.TotalAlloc >= previousTotalAlloc {
						point.AllocationRate = float64(telemetry.TotalAlloc-previousTotalAlloc) / interval.Seconds()
					}
					previousTotalAlloc = telemetry.TotalAlloc
					summary.HeapBytesMax = max(summary.HeapBytesMax, point.HeapBytes)
					summary.AllocationRateMax = max(summary.AllocationRateMax, point.AllocationRate)
					summary.GoroutinesMax = max(summary.GoroutinesMax, point.Goroutines)
					summary.QueueBytesMax = max(summary.QueueBytesMax, point.QueueBytes)
					summary.QueueRecordsMax = max(summary.QueueRecordsMax, point.QueueRecords)
					summary.DroppedLogsMax = max(summary.DroppedLogsMax, point.DroppedLogs)
				}
			}
			points = append(points, point)
		}
	}
}

type telemetrySample struct {
	HeapBytes      uint64 `json:"heap_bytes"`
	TotalAlloc     uint64 `json:"total_alloc_bytes"`
	GCCount        uint32 `json:"gc_count"`
	Goroutines     int    `json:"goroutines"`
	QueueBytes     uint64 `json:"log_queue_bytes"`
	QueueRecords   uint64 `json:"log_queue_records"`
	DroppedLogs    uint64 `json:"dropped_logs"`
	CacheHits      uint64 `json:"cache_hits"`
	CacheMisses    uint64 `json:"cache_misses"`
	CacheEvictions uint64 `json:"cache_evictions"`
}

func fetchTelemetry(ctx context.Context, client *http.Client, url string) (telemetrySample, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return telemetrySample{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return telemetrySample{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return telemetrySample{}, fmt.Errorf("telemetry status %d", response.StatusCode)
	}
	var sample telemetrySample
	return sample, json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&sample)
}
