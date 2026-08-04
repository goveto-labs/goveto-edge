package agentbench

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	quic "github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/shirou/gopsutil/v4/process"
	"golang.org/x/net/http2"
)

type closeRoundTripper interface {
	http.RoundTripper
	Close() error
}

var benchmarkUDPAddresses sync.Map

type runState struct {
	cleanupOnly   bool
	requests      atomic.Uint64
	successes     atomic.Uint64
	failures      atomic.Uint64
	bytes         atomic.Uint64
	mu            sync.Mutex
	latencies     []time.Duration
	handshakes    []time.Duration
	ttfb          []time.Duration
	protocols     map[string]uint64
	headers       map[string]map[string]uint64
	statuses      map[int]uint64
	errors        []string
	cleanupErrors []string
	errorCounts   map[string]uint64
}

func RunBenchmark(ctx context.Context, config Config) (Report, error) {
	if err := validateConfig(&config); err != nil {
		return Report{}, err
	}
	if config.UniqueQuery && config.UniqueQueryCardinality == 0 && config.UniqueQueryNamespace == "" {
		config.UniqueQueryNamespace = fmt.Sprintf("%x", time.Now().UnixNano())
	}
	if config.AgentMetricsURL != "" {
		if err := setBenchmarkVariant(ctx, config.AgentMetricsURL, config.Variant); err != nil {
			return Report{}, err
		}
	}
	report := Report{
		SchemaVersion: SchemaVersion,
		RunnerID:      config.RunnerID,
		GeneratedAt:   time.Now().UTC(),
		Platform:      CollectPlatform(),
		Scenario: Scenario{
			Suite: config.Suite, Name: config.Scenario, Method: config.Method, Protocol: config.Protocol, URL: config.URL,
			Concurrency: config.Concurrency, DurationMS: config.Duration.Milliseconds(), WarmupMS: config.Warmup.Milliseconds(),
			Repeats: config.Repeats, NewConnection: config.NewConnection, ExpectedStatus: config.ExpectedStatus,
			AllowedStatuses: config.AllowedStatuses, MinStatusCounts: config.MinStatusCounts, MaxStatusCounts: config.MaxStatusCounts,
			ExpectedSHA256: config.ExpectedSHA256, ExpectedHeaders: config.ExpectedHeaders,
			AllowedHeaders: config.AllowedHeaders, MaxHeaderRatios: config.MaxHeaderRatios,
			RequestHeaders: config.RequestHeaders, CaptureHeaders: config.CaptureHeaders, UniqueQuery: config.UniqueQuery,
			UniqueQueryCardinality: config.UniqueQueryCardinality,
			CooldownMS:             config.Cooldown.Milliseconds(), CapacityProbe: config.CapacityProbe,
			MinCacheHits: config.MinCacheHits, MinCacheMisses: config.MinCacheMisses, MinCacheEvictions: config.MinCacheEvictions,
			MaxCapturedValues: config.MaxCapturedValues, MaxLoadCPUPercent: config.MaxLoadCPUPercent,
			MaxAgentRSSBytes: config.MaxAgentRSSBytes, MaxAgentRSSGrowthBytes: config.MaxAgentRSSGrowthBytes,
			MinRPS: config.MinRPS, MaxP99MS: config.MaxP99MS, MaxAllocationBytesPerRequest: config.MaxAllocationBytesPerRequest,
			PostCooldownGC: config.AgentGCURL != "" && config.Cooldown > 0,
			Variant:        config.Variant, RequireCompleteAccessLogs: config.RequireCompleteAccessLogs, RequireCacheWritesDrained: config.RequireCacheWritesDrained,
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
	report.ErrorCounts = aggregateErrorCounts(report.Runs)
	report.Validity = validateRuns(report.Runs, loadCPUMax, config.MaxLoadCPUPercent, config.CapacityProbe)
	report.Validity = ValidateResourceExpectations(report.Validity, report.Runs, config)
	return report, nil
}

func runOnce(ctx context.Context, config Config, index int) (Run, float64, error) {
	if config.UniqueQueryNamespace != "" {
		config.UniqueQueryNamespace = fmt.Sprintf("%s-r%d", config.UniqueQueryNamespace, index)
	}
	sharedQUIC, closeSharedQUIC, err := newSharedQUICTransport(config)
	if err != nil {
		return Run{}, 0, err
	}
	transport, closeTransport, err := newTransport(config, sharedQUIC)
	if err != nil {
		return Run{}, 0, errors.Join(err, closeSharedQUIC())
	}
	var cleanupOnce sync.Once
	var cleanupErr error
	cleanup := func() error {
		cleanupOnce.Do(func() {
			cleanupErr = errors.Join(closeTransport(), closeSharedQUIC())
		})
		return cleanupErr
	}
	defer cleanup()
	client := &http.Client{Transport: transport, Timeout: config.RequestTimeout}
	var requestSequence atomic.Uint64
	warmupState := &runState{cleanupOnly: true}
	var environmentErrors []string

	if config.Warmup > 0 {
		warmContext, cancel := context.WithTimeout(ctx, config.Warmup)
		runWorkersWithRequestContext(warmContext, warmContext, config, client, warmupState, &requestSequence, sharedQUIC)
		cancel()
	}
	if config.RequireCompleteAccessLogs {
		if drainErr := waitForAccessBufferDrain(ctx, config.AgentMetricsURL, 10*time.Second); drainErr != nil {
			environmentErrors = append(environmentErrors, drainErr.Error())
		}
	}

	state := &runState{protocols: make(map[string]uint64), headers: make(map[string]map[string]uint64), statuses: make(map[int]uint64), errorCounts: make(map[string]uint64)}
	for _, cleanupErr := range warmupState.cleanupErrorSamples() {
		state.recordCleanupError(errors.New(cleanupErr))
	}
	measureContext, cancelSampling := context.WithCancel(ctx)
	defer cancelSampling()
	samplesDone := make(chan struct{})
	samplesReady := make(chan struct{})
	var samples []TimeSeriesPoint
	var resources ResourceSummary
	var loadCPUMax float64
	go func() {
		defer close(samplesDone)
		samples, resources, loadCPUMax = sampleResources(measureContext, config.AgentPID, config.AgentMetricsURL, config.SampleInterval, state, samplesReady)
	}()
	select {
	case <-samplesReady:
	case <-ctx.Done():
		cancelSampling()
		<-samplesDone
		return Run{}, 0, ctx.Err()
	}
	started := time.Now().UTC()
	dispatchContext, stopDispatch := context.WithTimeout(measureContext, config.Duration)
	// Stop dispatching at the measurement boundary, but allow requests already in
	// flight to complete under the normal per-request timeout.
	runWorkersWithRequestContext(dispatchContext, measureContext, config, client, state, &requestSequence, sharedQUIC)
	stopDispatch()
	if err = cleanup(); err != nil {
		state.recordCleanupError(err)
	}
	if config.RequireCacheWritesDrained {
		if drainErr := triggerCacheControl(ctx, config.AgentMetricsURL, "/cache/drain"); drainErr != nil {
			environmentErrors = append(environmentErrors, drainErr.Error())
		}
	}
	if config.Cooldown > 0 {
		timer := time.NewTimer(config.Cooldown)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}
	}
	cancelSampling()
	<-samplesDone
	naturalEnd := captureResourcePoint(config.AgentPID, config.AgentMetricsURL, state, "cooldown_end")
	if !naturalEnd.At.IsZero() {
		samples = append(samples, naturalEnd)
		applyNaturalEnd(&resources, naturalEnd)
	}
	if config.MaxAllocationBytesPerRequest > 0 || config.RequireCacheWritesDrained || config.RequireCompleteAccessLogs {
		if !resources.telemetryBaselineCaptured || !naturalEnd.telemetryCaptured {
			environmentErrors = append(environmentErrors, "required benchmark telemetry was unavailable at the measurement boundary")
		}
	}
	if config.AgentGCURL != "" && config.Cooldown > 0 {
		if gcErr := triggerPostCooldownGC(ctx, config.AgentGCURL); gcErr != nil {
			environmentErrors = append(environmentErrors, gcErr.Error())
		} else {
			timer := time.NewTimer(time.Second)
			select {
			case <-timer.C:
			case <-ctx.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
			}
			postGC := captureResourcePoint(config.AgentPID, config.AgentMetricsURL, state, "post_gc")
			if !postGC.At.IsZero() {
				samples = append(samples, postGC)
				if (config.AgentPID > 0 && postGC.RSSBytes == 0) || (config.AgentMetricsURL != "" && postGC.HeapBytes == 0) {
					environmentErrors = append(environmentErrors, "post-cooldown GC completed but its resource snapshot was incomplete")
				} else {
					applyPostGC(&resources, postGC)
				}
			}
		}
	}
	if resources.RSSBytesMax > resources.RSSBytesStart {
		resources.RSSBytesGrowth = resources.RSSBytesMax - resources.RSSBytesStart
	}
	metrics, failures, errorCounts := state.metrics(config.Duration)
	return Run{Index: index, StartedAt: started, Metrics: metrics, Resources: resources, Samples: samples, Errors: failures, ErrorCounts: errorCounts, CleanupErrors: state.cleanupErrorSamples(), EnvironmentErrors: environmentErrors}, loadCPUMax, nil
}

func runWorkers(ctx context.Context, config Config, sharedClient *http.Client, state *runState, sequence *atomic.Uint64) {
	runWorkersWithRequestContext(ctx, ctx, config, sharedClient, state, sequence, nil)
}

func runWorkersWithRequestContext(dispatchContext, requestContext context.Context, config Config, sharedClient *http.Client, state *runState, sequence *atomic.Uint64, sharedQUIC ...*quic.Transport) {
	var workers sync.WaitGroup
	for range config.Concurrency {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for dispatchContext.Err() == nil {
				client := sharedClient
				closeClient := func() error { return nil }
				if config.NewConnection {
					var quicTransport *quic.Transport
					if len(sharedQUIC) > 0 {
						quicTransport = sharedQUIC[0]
					}
					transport, closeTransport, err := newTransport(config, quicTransport)
					if err != nil {
						if state != nil {
							state.recordFailure(err)
						}
						return
					}
					client = &http.Client{Transport: transport, Timeout: config.RequestTimeout}
					closeClient = closeTransport
				}
				result := executeRequest(requestContext, client, config, sequence)
				if err := closeClient(); err != nil && state != nil {
					state.recordCleanupError(err)
				}
				if state != nil && !ignoreMeasurementCancellation(requestContext, result.err) {
					state.record(result)
				}
			}
		}()
	}
	workers.Wait()
}

func ignoreMeasurementCancellation(ctx context.Context, err error) bool {
	if ctx.Err() == nil || err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var streamError *quic.StreamError
	return errors.As(err, &streamError) && !streamError.Remote && streamError.ErrorCode == quic.StreamErrorCode(http3.ErrCodeRequestCanceled)
}

type requestResult struct {
	latency   time.Duration
	handshake time.Duration
	ttfb      time.Duration
	bytes     uint64
	protocol  string
	headers   map[string]string
	status    int
	err       error
}

func executeRequest(ctx context.Context, client *http.Client, config Config, sequences ...*atomic.Uint64) requestResult {
	method := config.Method
	if method == "" {
		method = http.MethodGet
	}
	request, err := http.NewRequestWithContext(ctx, method, config.URL, nil)
	if err != nil {
		return requestResult{err: err}
	}
	if config.Host != "" {
		request.Host = config.Host
	}
	request.Header.Set("Accept-Encoding", "identity")
	for name, value := range config.RequestHeaders {
		request.Header.Set(name, value)
	}
	if config.UniqueQuery {
		sequence := uint64(1)
		if len(sequences) > 0 && sequences[0] != nil {
			sequence = sequences[0].Add(1)
		}
		if config.UniqueQueryCardinality > 0 {
			sequence = (sequence-1)%uint64(config.UniqueQueryCardinality) + 1
		}
		query := request.URL.Query()
		value := fmt.Sprintf("%d", sequence)
		if config.UniqueQueryNamespace != "" {
			value = config.UniqueQueryNamespace + "-" + value
		}
		query.Set("_bench", value)
		request.URL.RawQuery = query.Encode()
	}
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
	var errorBody limitedBuffer
	statusOK := statusAllowed(config, response.StatusCode)
	if !statusOK {
		destination = io.MultiWriter(destination, &errorBody)
	}
	written, readErr := io.Copy(destination, response.Body)
	result := requestResult{latency: time.Since(started), handshake: handshake, ttfb: ttfb, bytes: uint64(max(written, 0)), protocol: response.Proto, headers: make(map[string]string, len(config.CaptureHeaders)), status: response.StatusCode}
	for _, name := range config.CaptureHeaders {
		result.headers[http.CanonicalHeaderKey(name)] = response.Header.Get(name)
	}
	if readErr != nil {
		result.err = fmt.Errorf("read response: %w (bytes=%d content_length=%d x_cache=%q)", readErr, written, response.ContentLength, response.Header.Get("X-Cache"))
		return result
	}
	if !statusOK {
		if len(config.AllowedStatuses) > 0 {
			result.err = fmt.Errorf("status %d, want one of %v, body %q", response.StatusCode, config.AllowedStatuses, errorBody.String())
		} else {
			result.err = fmt.Errorf("status %d, want %d, body %q", response.StatusCode, config.ExpectedStatus, errorBody.String())
		}
		return result
	}
	if expected := protocolName(config.Protocol); response.Proto != expected {
		result.err = fmt.Errorf("negotiated %s, want %s", response.Proto, expected)
		return result
	}
	if digest != nil && !strings.EqualFold(hex.EncodeToString(digest.Sum(nil)), config.ExpectedSHA256) {
		result.err = fmt.Errorf("response SHA-256 mismatch (got=%s bytes=%d content_length=%d x_cache=%q)", hex.EncodeToString(digest.Sum(nil)), written, response.ContentLength, response.Header.Get("X-Cache"))
		return result
	}
	for name, expected := range config.ExpectedHeaders {
		if actual := response.Header.Get(name); actual != expected {
			result.err = fmt.Errorf("header %s=%q, want %q", name, actual, expected)
			return result
		}
	}
	for name, allowed := range config.AllowedHeaders {
		actual := response.Header.Get(name)
		if !contains(allowed, actual) {
			result.err = fmt.Errorf("header %s=%q, want one of %q", name, actual, allowed)
			return result
		}
	}
	return result
}

func newTransport(config Config, sharedQUIC ...*quic.Transport) (http.RoundTripper, func() error, error) {
	tlsConfig := &tls.Config{InsecureSkipVerify: config.InsecureSkipVerify, ServerName: serverName(config)} // benchmark targets may use private test certificates.
	switch config.Protocol {
	case ProtocolH1:
		transport := &http.Transport{TLSClientConfig: tlsConfig, ForceAttemptHTTP2: false, MaxIdleConns: config.Concurrency * 2, MaxIdleConnsPerHost: config.Concurrency * 2, DisableCompression: true}
		return transport, func() error { transport.CloseIdleConnections(); return nil }, nil
	case ProtocolH2:
		transport := &http2.Transport{TLSClientConfig: tlsConfig, DisableCompression: true}
		return transport, func() error { transport.CloseIdleConnections(); return nil }, nil
	case ProtocolH3:
		transport := &http3.Transport{TLSClientConfig: tlsConfig, DisableCompression: true}
		if len(sharedQUIC) > 0 && sharedQUIC[0] != nil {
			transport.Dial = func(ctx context.Context, address string, tlsConfig *tls.Config, quicConfig *quic.Config) (*quic.Conn, error) {
				remote, err := resolveBenchmarkUDPAddress(address)
				if err != nil {
					return nil, err
				}
				return sharedQUIC[0].Dial(ctx, remote, tlsConfig, quicConfig)
			}
		}
		return transport, transport.Close, nil
	default:
		return nil, func() error { return nil }, fmt.Errorf("unsupported protocol %q", config.Protocol)
	}
}

func resolveBenchmarkUDPAddress(address string) (*net.UDPAddr, error) {
	if cached, ok := benchmarkUDPAddresses.Load(address); ok {
		return cached.(*net.UDPAddr), nil
	}
	resolved, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		return nil, err
	}
	actual, _ := benchmarkUDPAddresses.LoadOrStore(address, resolved)
	return actual.(*net.UDPAddr), nil
}

func newSharedQUICTransport(config Config) (*quic.Transport, func() error, error) {
	if config.Protocol != ProtocolH3 || !config.NewConnection {
		return nil, func() error { return nil }, nil
	}
	connection, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero})
	if err != nil {
		return nil, func() error { return nil }, fmt.Errorf("open shared H3 UDP socket: %w", err)
	}
	transport := &quic.Transport{Conn: connection}
	var closeOnce sync.Once
	var closeErr error
	return transport, func() error {
		closeOnce.Do(func() {
			transportErr := transport.Close()
			connectionErr := connection.Close()
			if errors.Is(transportErr, net.ErrClosed) {
				transportErr = nil
			}
			if errors.Is(connectionErr, net.ErrClosed) {
				connectionErr = nil
			}
			closeErr = errors.Join(transportErr, connectionErr)
		})
		return closeErr
	}, nil
}

func validateConfig(config *Config) error {
	if strings.TrimSpace(config.RunnerID) == "" {
		config.RunnerID = "default"
	}
	if config.URL == "" {
		return errors.New("URL is required")
	}
	if config.Scenario == "" {
		return errors.New("scenario is required")
	}
	if config.Method == "" {
		config.Method = http.MethodGet
	}
	config.Method = strings.ToUpper(config.Method)
	if config.UniqueQueryCardinality < 0 {
		return errors.New("unique query cardinality must not be negative")
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
	if config.MaxLoadCPUPercent <= 0 {
		config.MaxLoadCPUPercent = 85
	}
	if config.ExpectedStatus == 0 {
		config.ExpectedStatus = http.StatusOK
	}
	if config.Variant == "" {
		config.Variant = VariantFull
	}
	if config.Variant != VariantFull && config.Variant != VariantControl {
		return fmt.Errorf("variant must be %q or %q", VariantFull, VariantControl)
	}
	if (config.Variant == VariantControl || config.RequireCompleteAccessLogs) && config.AgentMetricsURL == "" {
		return errors.New("control and complete access log modes require agent metrics")
	}
	for _, status := range config.AllowedStatuses {
		if status < 100 || status > 599 {
			return fmt.Errorf("invalid allowed HTTP status %d", status)
		}
	}
	for status := range config.MinStatusCounts {
		if status < 100 || status > 599 {
			return fmt.Errorf("invalid minimum HTTP status %d", status)
		}
	}
	for status := range config.MaxStatusCounts {
		if status < 100 || status > 599 {
			return fmt.Errorf("invalid maximum HTTP status %d", status)
		}
	}
	if config.ExpectedHeaders == nil {
		config.ExpectedHeaders = map[string]string{}
	}
	if config.AllowedHeaders == nil {
		config.AllowedHeaders = map[string][]string{}
	}
	if config.MaxHeaderRatios == nil {
		config.MaxHeaderRatios = map[string]map[string]float64{}
	}
	for name, values := range config.AllowedHeaders {
		if strings.TrimSpace(name) == "" || len(values) == 0 {
			return errors.New("allowed response headers require a name and at least one value")
		}
		config.CaptureHeaders = appendUniqueHeader(config.CaptureHeaders, name)
	}
	for name, values := range config.MaxHeaderRatios {
		config.CaptureHeaders = appendUniqueHeader(config.CaptureHeaders, name)
		for value, ratio := range values {
			if value == "" || ratio < 0 || ratio > 1 {
				return fmt.Errorf("invalid maximum header ratio %s=%q:%g", name, value, ratio)
			}
		}
	}
	if config.RequestHeaders == nil {
		config.RequestHeaders = map[string]string{}
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

const maxFailureBodyBytes = 4 << 10

type limitedBuffer struct{ bytes.Buffer }

func (buffer *limitedBuffer) Write(value []byte) (int, error) {
	written := len(value)
	if buffer.Len() < maxFailureBodyBytes {
		remaining := maxFailureBodyBytes - buffer.Len()
		_, _ = buffer.Buffer.Write(value[:min(len(value), remaining)])
	}
	return written, nil
}

func (buffer *limitedBuffer) WriteString(value string) (int, error) {
	return buffer.Write([]byte(value))
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func statusAllowed(config Config, status int) bool {
	if len(config.AllowedStatuses) == 0 {
		return status == config.ExpectedStatus
	}
	for _, allowed := range config.AllowedStatuses {
		if status == allowed {
			return true
		}
	}
	return false
}

func appendUniqueHeader(values []string, name string) []string {
	canonical := http.CanonicalHeaderKey(name)
	for _, value := range values {
		if http.CanonicalHeaderKey(value) == canonical {
			return values
		}
	}
	return append(values, canonical)
}

func (state *runState) record(result requestResult) {
	if state.cleanupOnly {
		return
	}
	state.requests.Add(1)
	state.bytes.Add(result.bytes)
	if result.status != 0 {
		state.mu.Lock()
		if state.statuses == nil {
			state.statuses = make(map[int]uint64)
		}
		state.statuses[result.status]++
		state.mu.Unlock()
	}
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
	for name, value := range result.headers {
		if state.headers[name] == nil {
			state.headers[name] = make(map[string]uint64)
		}
		state.headers[name][value]++
	}
	state.mu.Unlock()
}

func (state *runState) recordFailure(err error) {
	if state.cleanupOnly {
		return
	}
	state.failures.Add(1)
	state.mu.Lock()
	if state.errorCounts == nil {
		state.errorCounts = make(map[string]uint64)
	}
	state.errorCounts[classifyFailure(err)]++
	if len(state.errors) < 20 {
		state.errors = append(state.errors, err.Error())
	}
	state.mu.Unlock()
}

func (state *runState) recordCleanupError(err error) {
	if err == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.errorCounts == nil {
		state.errorCounts = make(map[string]uint64)
	}
	state.errorCounts["cleanup"]++
	if len(state.cleanupErrors) < 20 {
		state.cleanupErrors = append(state.cleanupErrors, err.Error())
	}
}

func (state *runState) cleanupErrorSamples() []string {
	state.mu.Lock()
	defer state.mu.Unlock()
	return append([]string(nil), state.cleanupErrors...)
}

func (state *runState) metrics(elapsed time.Duration) (Metrics, []string, map[string]uint64) {
	state.mu.Lock()
	defer state.mu.Unlock()
	seconds := elapsed.Seconds()
	requests, successes, failures := state.requests.Load(), state.successes.Load(), state.failures.Load()
	result := Metrics{Requests: requests, Successes: successes, Failures: failures, Bytes: state.bytes.Load(), ResponseHeaders: state.headers, HTTPStatusCounts: cloneStatusCounts(state.statuses)}
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
	return result, append([]string(nil), state.errors...), cloneCounts(state.errorCounts)
}

func classifyFailure(err error) string {
	if err == nil {
		return "unknown"
	}
	var streamError *quic.StreamError
	if errors.As(err, &streamError) && !streamError.Remote && streamError.ErrorCode == quic.StreamErrorCode(http3.ErrCodeRequestCanceled) {
		return "h3_local_cancel"
	}
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "i/o timeout") {
		return "timeout"
	}
	message := err.Error()
	switch {
	case strings.HasPrefix(message, "status 5"):
		return "http_5xx"
	case strings.HasPrefix(message, "status "):
		return "http_status"
	case strings.HasPrefix(message, "negotiated "):
		return "protocol_mismatch"
	case strings.Contains(message, "SHA-256 mismatch"):
		return "body_integrity"
	case strings.HasPrefix(message, "header "):
		return "header_mismatch"
	case strings.HasPrefix(message, "read response:"):
		return "response_read"
	case strings.Contains(message, "dial "):
		return "dial"
	case strings.Contains(message, "tls:") || strings.Contains(message, "x509:"):
		return "tls"
	case strings.Contains(message, "connection reset") || strings.Contains(message, "stream reset"):
		return "reset"
	default:
		return "transport"
	}
}

func capacityFailureCount(counts map[string]uint64) uint64 {
	var total uint64
	for _, class := range []string{"timeout", "response_read", "dial", "tls", "reset", "transport"} {
		total += counts[class]
	}
	return total
}

func aggregateErrorCounts(runs []Run) map[string]uint64 {
	counts := make(map[string]uint64)
	for _, run := range runs {
		for class, count := range run.ErrorCounts {
			counts[class] += count
		}
	}
	if len(counts) == 0 {
		return nil
	}
	return counts
}

func cloneCounts(values map[string]uint64) map[string]uint64 {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]uint64, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func cloneStatusCounts(values map[int]uint64) map[int]uint64 {
	if len(values) == 0 {
		return nil
	}
	result := make(map[int]uint64, len(values))
	for status, count := range values {
		result[status] = count
	}
	return result
}

func durationMS(value time.Duration) float64 { return float64(value) / float64(time.Millisecond) }

func sampleResources(ctx context.Context, pid int32, metricsURL string, interval time.Duration, state *runState, ready chan<- struct{}) ([]TimeSeriesPoint, ResourceSummary, float64) {
	loadProcess, _ := process.NewProcess(int32(os.Getpid()))
	var target *process.Process
	if pid > 0 {
		target, _ = process.NewProcess(pid)
	}
	var points []TimeSeriesPoint
	var summary ResourceSummary
	var loadCPUMax float64
	var previousRequests uint64
	metricsClient := &http.Client{Timeout: min(interval, 2*time.Second)}
	var baselineAt time.Time
	var previousTargetCPU float64
	var hasTargetCPU bool
	var ioBaselineRead, ioBaselineWrite uint64
	var hasIOBaseline bool
	var telemetryBaseline telemetrySample
	var previousTelemetry telemetrySample
	var previousTelemetryAt time.Time
	var hasTelemetryBaseline bool
	if metricsURL != "" {
		var err error
		telemetryBaseline, err = fetchTelemetry(ctx, metricsClient, metricsURL)
		hasTelemetryBaseline = err == nil
		if hasTelemetryBaseline {
			summary.telemetryBaselineCaptured = true
			previousTelemetry = telemetryBaseline
			summary.HeapBytesStart = telemetryBaseline.HeapBytes
			summary.HeapInuseBytesStart = telemetryBaseline.HeapInuseBytes
			summary.HeapIdleBytesStart = telemetryBaseline.HeapIdleBytes
			summary.HeapReleasedBytesStart = telemetryBaseline.HeapReleasedBytes
			summary.GoroutinesStart = telemetryBaseline.Goroutines
			summary.memoryDroppedLogsStart = telemetryBaseline.MemoryDroppedLogs
			summary.diskDroppedLogsStart = telemetryBaseline.DiskDroppedLogs
			summary.committedBatchesStart = telemetryBaseline.CommittedBatches
			summary.committedRecordsStart = telemetryBaseline.CommittedRecords
			summary.totalAllocStart = telemetryBaseline.TotalAlloc
			summary.cacheWriteRejectionsStart = telemetryBaseline.CacheWriteRejections
			summary.cacheWriteBatchesStart = telemetryBaseline.CacheWriteBatches
			summary.cacheWriteObjectsStart = telemetryBaseline.CacheWriteObjects
		}
	}
	baselineAt = time.Now()
	previousTelemetryAt = baselineAt
	if target != nil {
		if memory, err := target.MemoryInfo(); err == nil {
			summary.RSSBytesStart = memory.RSS
		}
		summary.FDsStart, _ = target.NumFDs()
		if connections, err := target.Connections(); err == nil {
			summary.ConnectionsStart = len(connections)
		}
		if times, err := target.Times(); err == nil {
			previousTargetCPU = times.User + times.System
			hasTargetCPU = true
		}
		if counters, err := target.IOCounters(); err == nil {
			ioBaselineRead = counters.ReadBytes
			ioBaselineWrite = counters.WriteBytes
			hasIOBaseline = true
		}
	}
	if loadProcess != nil {
		_, _ = loadProcess.CPUPercent()
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	close(ready)
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
				if times, err := target.Times(); err == nil {
					sampledAt := time.Now()
					currentCPU := times.User + times.System
					elapsed := sampledAt.Sub(baselineAt).Seconds()
					if hasTargetCPU && elapsed > 0 && currentCPU >= previousTargetCPU {
						point.CPUPercent = (currentCPU - previousTargetCPU) / elapsed * 100
					}
					previousTargetCPU = currentCPU
					hasTargetCPU = true
					baselineAt = sampledAt
				}
				if memory, err := target.MemoryInfo(); err == nil {
					point.RSSBytes = memory.RSS
				}
				point.FDs, _ = target.NumFDs()
				if connections, err := target.Connections(); err == nil {
					point.Connections = len(connections)
				}
				if counters, err := target.IOCounters(); err == nil {
					if hasIOBaseline {
						summary.ReadBytes = counterDelta(counters.ReadBytes, ioBaselineRead)
						summary.WriteBytes = counterDelta(counters.WriteBytes, ioBaselineWrite)
					}
				}
				summary.CPUPercentMax = max(summary.CPUPercentMax, point.CPUPercent)
				summary.RSSBytesMax = max(summary.RSSBytesMax, point.RSSBytes)
				summary.FDsMax = max(summary.FDsMax, point.FDs)
				summary.ConnectionsMax = max(summary.ConnectionsMax, point.Connections)
				summary.RSSBytesEnd = point.RSSBytes
				summary.FDsEnd = point.FDs
				summary.ConnectionsEnd = point.Connections
			}
			if metricsURL != "" {
				if telemetry, err := fetchTelemetry(ctx, metricsClient, metricsURL); err == nil {
					point.telemetryCaptured = true
					point.HeapBytes = telemetry.HeapBytes
					point.HeapInuseBytes = telemetry.HeapInuseBytes
					point.HeapIdleBytes = telemetry.HeapIdleBytes
					point.HeapReleasedBytes = telemetry.HeapReleasedBytes
					point.GCCount = telemetry.GCCount
					point.Goroutines = telemetry.Goroutines
					point.QueueBytes = telemetry.QueueBytes
					point.QueueRecords = telemetry.QueueRecords
					point.BufferBytes = telemetry.BufferBytes
					point.BufferRecords = telemetry.BufferRecords
					point.AverageBatchSize = telemetry.AverageBatchSize
					point.LastPersistError = telemetry.LastPersistError
					if !telemetry.LastPersistSuccess.IsZero() {
						lastSuccess := telemetry.LastPersistSuccess
						point.LastPersistSuccess = &lastSuccess
					}
					if hasTelemetryBaseline {
						point.DroppedLogs = counterDelta(telemetry.DroppedLogs, telemetryBaseline.DroppedLogs)
						point.MemoryDroppedLogs = counterDelta(telemetry.MemoryDroppedLogs, telemetryBaseline.MemoryDroppedLogs)
						point.DiskDroppedLogs = counterDelta(telemetry.DiskDroppedLogs, telemetryBaseline.DiskDroppedLogs)
						point.CommittedBatches = counterDelta(telemetry.CommittedBatches, telemetryBaseline.CommittedBatches)
						point.CommittedRecords = counterDelta(telemetry.CommittedRecords, telemetryBaseline.CommittedRecords)
						point.CacheHits = counterDelta(telemetry.CacheHits, telemetryBaseline.CacheHits)
						point.CacheMisses = counterDelta(telemetry.CacheMisses, telemetryBaseline.CacheMisses)
						point.CacheEvictions = counterDelta(telemetry.CacheEvictions, telemetryBaseline.CacheEvictions)
					}
					point.TotalAllocBytes = telemetry.TotalAlloc
					point.CacheWriteQueueDepth = telemetry.CacheWriteQueueDepth
					point.CacheWriteQueueBytes = telemetry.CacheWriteQueueBytes
					point.CacheWriteQueueDepthMax = telemetry.CacheWriteQueueDepthMax
					point.CacheWriteQueueBytesMax = telemetry.CacheWriteQueueBytesMax
					point.CacheWriteRejections = telemetry.CacheWriteRejections
					point.CacheWriteBatches = telemetry.CacheWriteBatches
					point.CacheWriteObjects = telemetry.CacheWriteObjects
					point.CacheAverageWriteBatchSize = telemetry.CacheAverageWriteBatchSize
					point.CacheWriteCommitLatencyMS = telemetry.CacheWriteCommitLatencyMS
					point.CacheInflightWrites = telemetry.CacheInflightWrites
					allocationElapsed := time.Since(previousTelemetryAt).Seconds()
					if hasTelemetryBaseline && allocationElapsed > 0 && telemetry.TotalAlloc >= previousTelemetry.TotalAlloc {
						point.AllocationRate = float64(telemetry.TotalAlloc-previousTelemetry.TotalAlloc) / allocationElapsed
					}
					previousTelemetry = telemetry
					previousTelemetryAt = time.Now()
					summary.HeapBytesMax = max(summary.HeapBytesMax, point.HeapBytes)
					summary.HeapInuseBytesMax = max(summary.HeapInuseBytesMax, point.HeapInuseBytes)
					summary.HeapIdleBytesMax = max(summary.HeapIdleBytesMax, point.HeapIdleBytes)
					summary.HeapReleasedBytesMax = max(summary.HeapReleasedBytesMax, point.HeapReleasedBytes)
					summary.AllocationRateMax = max(summary.AllocationRateMax, point.AllocationRate)
					summary.GoroutinesMax = max(summary.GoroutinesMax, point.Goroutines)
					summary.HeapBytesEnd = point.HeapBytes
					summary.GoroutinesEnd = point.Goroutines
					summary.QueueBytesMax = max(summary.QueueBytesMax, point.QueueBytes)
					summary.QueueRecordsMax = max(summary.QueueRecordsMax, point.QueueRecords)
					summary.DroppedLogsMax = max(summary.DroppedLogsMax, point.DroppedLogs)
					summary.BufferBytesMax = max(summary.BufferBytesMax, point.BufferBytes)
					summary.BufferRecordsMax = max(summary.BufferRecordsMax, point.BufferRecords)
					summary.MemoryDroppedLogsDelta = point.MemoryDroppedLogs
					summary.DiskDroppedLogsDelta = point.DiskDroppedLogs
					summary.CommittedBatchesDelta = point.CommittedBatches
					summary.CommittedRecordsDelta = point.CommittedRecords
					summary.AverageBatchSize = point.AverageBatchSize
					summary.LastPersistError = point.LastPersistError
					summary.LastPersistSuccess = point.LastPersistSuccess
					summary.CacheHitsDelta = point.CacheHits
					summary.CacheMissesDelta = point.CacheMisses
					summary.CacheEvictionsDelta = point.CacheEvictions
					summary.TotalAllocBytes = point.TotalAllocBytes
					summary.CacheWriteQueueDepthMax = max(summary.CacheWriteQueueDepthMax, point.CacheWriteQueueDepthMax)
					summary.CacheWriteQueueBytesMax = max(summary.CacheWriteQueueBytesMax, point.CacheWriteQueueBytesMax)
					summary.CacheAverageWriteBatchSize = point.CacheAverageWriteBatchSize
					summary.CacheWriteCommitLatencyMS = point.CacheWriteCommitLatencyMS
				}
			}
			points = append(points, point)
		}
	}
}

func counterDelta(current, baseline uint64) uint64 {
	if current < baseline {
		return current
	}
	return current - baseline
}

type telemetrySample struct {
	HeapBytes                  uint64    `json:"heap_bytes"`
	HeapInuseBytes             uint64    `json:"heap_inuse_bytes"`
	HeapIdleBytes              uint64    `json:"heap_idle_bytes"`
	HeapReleasedBytes          uint64    `json:"heap_released_bytes"`
	TotalAlloc                 uint64    `json:"total_alloc_bytes"`
	GCCount                    uint32    `json:"gc_count"`
	Goroutines                 int       `json:"goroutines"`
	QueueBytes                 uint64    `json:"log_queue_bytes"`
	QueueRecords               uint64    `json:"log_queue_records"`
	BufferBytes                uint64    `json:"log_buffer_bytes"`
	BufferRecords              uint64    `json:"log_buffer_records"`
	DroppedLogs                uint64    `json:"dropped_logs"`
	MemoryDroppedLogs          uint64    `json:"memory_dropped_logs"`
	DiskDroppedLogs            uint64    `json:"disk_dropped_logs"`
	CommittedBatches           uint64    `json:"committed_log_batches"`
	CommittedRecords           uint64    `json:"committed_log_records"`
	AverageBatchSize           float64   `json:"average_log_batch_size"`
	LastPersistError           string    `json:"last_log_persist_error"`
	LastPersistSuccess         time.Time `json:"last_log_persist_success,omitempty"`
	CacheHits                  uint64    `json:"cache_hits"`
	CacheMisses                uint64    `json:"cache_misses"`
	CacheEvictions             uint64    `json:"cache_evictions"`
	CacheWriteQueueDepth       uint64    `json:"cache_write_queue_depth"`
	CacheWriteQueueBytes       uint64    `json:"cache_write_queue_bytes"`
	CacheWriteQueueDepthMax    uint64    `json:"cache_write_queue_depth_max"`
	CacheWriteQueueBytesMax    uint64    `json:"cache_write_queue_bytes_max"`
	CacheWriteRejections       uint64    `json:"cache_write_queue_rejections"`
	CacheWriteBatches          uint64    `json:"cache_write_batches"`
	CacheWriteObjects          uint64    `json:"cache_write_objects_committed"`
	CacheAverageWriteBatchSize float64   `json:"cache_average_write_batch_size"`
	CacheWriteCommitLatencyMS  float64   `json:"cache_write_commit_latency_ms"`
	CacheInflightWrites        uint64    `json:"cache_inflight_writes"`
}

func captureResourcePoint(pid int32, metricsURL string, state *runState, phase string) TimeSeriesPoint {
	if pid <= 0 && metricsURL == "" {
		return TimeSeriesPoint{}
	}
	point := TimeSeriesPoint{At: time.Now().UTC(), Phase: phase}
	if state != nil {
		point.Requests = state.requests.Load()
		point.Failures = state.failures.Load()
	}
	if pid > 0 {
		if target, err := process.NewProcess(pid); err == nil {
			if memory, memoryErr := target.MemoryInfo(); memoryErr == nil {
				point.RSSBytes = memory.RSS
			}
			point.FDs, _ = target.NumFDs()
			if connections, connectionErr := target.Connections(); connectionErr == nil {
				point.Connections = len(connections)
			}
		}
	}
	if metricsURL != "" {
		requestContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if telemetry, err := fetchTelemetry(requestContext, &http.Client{Timeout: 2 * time.Second}, metricsURL); err == nil {
			point.telemetryCaptured = true
			point.HeapBytes = telemetry.HeapBytes
			point.HeapInuseBytes = telemetry.HeapInuseBytes
			point.HeapIdleBytes = telemetry.HeapIdleBytes
			point.HeapReleasedBytes = telemetry.HeapReleasedBytes
			point.GCCount = telemetry.GCCount
			point.Goroutines = telemetry.Goroutines
			point.QueueBytes = telemetry.QueueBytes
			point.QueueRecords = telemetry.QueueRecords
			point.BufferBytes = telemetry.BufferBytes
			point.BufferRecords = telemetry.BufferRecords
			point.MemoryDroppedLogs = telemetry.MemoryDroppedLogs
			point.DiskDroppedLogs = telemetry.DiskDroppedLogs
			point.CommittedBatches = telemetry.CommittedBatches
			point.CommittedRecords = telemetry.CommittedRecords
			point.TotalAllocBytes = telemetry.TotalAlloc
			point.CacheWriteQueueDepth = telemetry.CacheWriteQueueDepth
			point.CacheWriteQueueBytes = telemetry.CacheWriteQueueBytes
			point.CacheWriteQueueDepthMax = telemetry.CacheWriteQueueDepthMax
			point.CacheWriteQueueBytesMax = telemetry.CacheWriteQueueBytesMax
			point.CacheWriteRejections = telemetry.CacheWriteRejections
			point.CacheWriteBatches = telemetry.CacheWriteBatches
			point.CacheWriteObjects = telemetry.CacheWriteObjects
			point.CacheAverageWriteBatchSize = telemetry.CacheAverageWriteBatchSize
			point.CacheWriteCommitLatencyMS = telemetry.CacheWriteCommitLatencyMS
			point.CacheInflightWrites = telemetry.CacheInflightWrites
		}
	}
	return point
}

func applyNaturalEnd(summary *ResourceSummary, point TimeSeriesPoint) {
	if point.RSSBytes > 0 {
		summary.RSSBytesEnd = point.RSSBytes
	}
	if point.FDs > 0 {
		summary.FDsEnd = point.FDs
	}
	if point.Connections > 0 || summary.ConnectionsStart > 0 {
		summary.ConnectionsEnd = point.Connections
	}
	if point.HeapBytes > 0 {
		summary.HeapBytesEnd = point.HeapBytes
		summary.HeapInuseBytesEnd = point.HeapInuseBytes
		summary.HeapIdleBytesEnd = point.HeapIdleBytes
		summary.HeapReleasedBytesEnd = point.HeapReleasedBytes
	}
	if point.Goroutines > 0 {
		summary.GoroutinesEnd = point.Goroutines
	}
	if !point.telemetryCaptured {
		return
	}
	summary.QueueBytesEnd = point.QueueBytes
	summary.QueueRecordsEnd = point.QueueRecords
	summary.BufferBytesEnd = point.BufferBytes
	summary.BufferRecordsEnd = point.BufferRecords
	summary.TotalAllocBytes = point.TotalAllocBytes
	summary.AllocatedBytes = counterDelta(point.TotalAllocBytes, summary.totalAllocStart)
	if point.Requests > 0 {
		summary.AllocationBytesPerRequest = float64(summary.AllocatedBytes) / float64(point.Requests)
	}
	summary.CacheWriteQueueDepthEnd = point.CacheWriteQueueDepth
	summary.CacheWriteQueueBytesEnd = point.CacheWriteQueueBytes
	summary.CacheInflightWritesEnd = point.CacheInflightWrites
	summary.CacheWriteQueueDepthMax = max(summary.CacheWriteQueueDepthMax, point.CacheWriteQueueDepthMax)
	summary.CacheWriteQueueBytesMax = max(summary.CacheWriteQueueBytesMax, point.CacheWriteQueueBytesMax)
	summary.CacheWriteRejectionsDelta = counterDelta(point.CacheWriteRejections, summary.cacheWriteRejectionsStart)
	summary.CacheWriteBatchesDelta = counterDelta(point.CacheWriteBatches, summary.cacheWriteBatchesStart)
	summary.CacheWriteObjectsDelta = counterDelta(point.CacheWriteObjects, summary.cacheWriteObjectsStart)
	if summary.CacheWriteBatchesDelta > 0 {
		summary.CacheAverageWriteBatchSize = float64(summary.CacheWriteObjectsDelta) / float64(summary.CacheWriteBatchesDelta)
	} else {
		summary.CacheAverageWriteBatchSize = point.CacheAverageWriteBatchSize
	}
	summary.CacheWriteCommitLatencyMS = point.CacheWriteCommitLatencyMS
	summary.MemoryDroppedLogsDelta = counterDelta(point.MemoryDroppedLogs, summary.memoryDroppedLogsStart)
	summary.DiskDroppedLogsDelta = counterDelta(point.DiskDroppedLogs, summary.diskDroppedLogsStart)
	summary.CommittedBatchesDelta = counterDelta(point.CommittedBatches, summary.committedBatchesStart)
	summary.CommittedRecordsDelta = counterDelta(point.CommittedRecords, summary.committedRecordsStart)
	summary.RSSBytesMax = max(summary.RSSBytesMax, point.RSSBytes)
	summary.HeapBytesMax = max(summary.HeapBytesMax, point.HeapBytes)
}

func setBenchmarkVariant(ctx context.Context, metricsURL string, variant Variant) error {
	endpoint, err := url.Parse(metricsURL)
	if err != nil {
		return fmt.Errorf("parse agent metrics URL: %w", err)
	}
	endpoint.Path = "/variant"
	query := endpoint.Query()
	query.Set("value", string(variant))
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), nil)
	if err != nil {
		return fmt.Errorf("create benchmark variant request: %w", err)
	}
	response, err := (&http.Client{Timeout: 2 * time.Second}).Do(request)
	if err != nil {
		return fmt.Errorf("set benchmark variant: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("set benchmark variant: status %d", response.StatusCode)
	}
	return nil
}

func triggerCacheControl(ctx context.Context, metricsURL, path string) error {
	if metricsURL == "" {
		return errors.New("cache write drain requires agent metrics URL")
	}
	endpoint, err := url.Parse(metricsURL)
	if err != nil {
		return fmt.Errorf("parse cache control URL: %w", err)
	}
	endpoint.Path = path
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), nil)
	if err != nil {
		return fmt.Errorf("create cache control request: %w", err)
	}
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		return fmt.Errorf("cache control %s: %w", path, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("cache control %s returned status %d", path, response.StatusCode)
	}
	return nil
}

func waitForAccessBufferDrain(ctx context.Context, metricsURL string, maximum time.Duration) error {
	drainContext, cancel := context.WithTimeout(ctx, maximum)
	defer cancel()
	client := &http.Client{Timeout: 2 * time.Second}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		telemetry, err := fetchTelemetry(drainContext, client, metricsURL)
		if err == nil && telemetry.BufferBytes == 0 && telemetry.BufferRecords == 0 {
			return nil
		}
		select {
		case <-drainContext.Done():
			return fmt.Errorf("access log buffer did not drain before measurement: %w", drainContext.Err())
		case <-ticker.C:
		}
	}
}

func applyPostGC(summary *ResourceSummary, point TimeSeriesPoint) {
	summary.RSSBytesPostGC = point.RSSBytes
	summary.HeapBytesPostGC = point.HeapBytes
	summary.HeapInuseBytesPostGC = point.HeapInuseBytes
	summary.HeapIdleBytesPostGC = point.HeapIdleBytes
	summary.HeapReleasedBytesPostGC = point.HeapReleasedBytes
}

func triggerPostCooldownGC(ctx context.Context, url string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return fmt.Errorf("create post-cooldown GC request: %w", err)
	}
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		return fmt.Errorf("trigger post-cooldown GC: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("trigger post-cooldown GC: status %d", response.StatusCode)
	}
	return nil
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
