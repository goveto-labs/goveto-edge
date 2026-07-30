package agentbench

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	quic "github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestExecuteRequestValidatesProtocolHashAndHeaders(t *testing.T) {
	body := "agent-benchmark"
	digest := sha256.Sum256([]byte(body))
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Proto: "HTTP/2.0", ProtoMajor: 2, Header: http.Header{"X-Cache": {"HIT"}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}
	result := executeRequest(t.Context(), client, Config{URL: "https://benchmark.example.test/asset", Protocol: ProtocolH2, ExpectedStatus: http.StatusOK, ExpectedSHA256: hex.EncodeToString(digest[:]), ExpectedHeaders: map[string]string{"X-Cache": "HIT"}})
	if result.err != nil {
		t.Fatal(result.err)
	}
	if result.protocol != "HTTP/2.0" || result.bytes != uint64(len(body)) {
		t.Fatalf("result=%+v", result)
	}
}

func TestExecuteRequestRejectsProtocolMismatch(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Proto: "HTTP/1.1", ProtoMajor: 1, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok")), Request: request}, nil
	})}
	result := executeRequest(t.Context(), client, Config{URL: "https://benchmark.example.test", Protocol: ProtocolH2, ExpectedStatus: http.StatusOK})
	if result.err == nil {
		t.Fatal("HTTP/1.1 response accepted as HTTP/2")
	}
}

func TestExecuteRequestAddsHeadersAndUniqueQuery(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Range") != "bytes=0-1023" || request.URL.Query().Get("_bench") != "test-r2-1" {
			t.Fatalf("request headers=%v query=%q", request.Header, request.URL.RawQuery)
		}
		return &http.Response{StatusCode: http.StatusOK, Proto: "HTTP/1.1", Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok")), Request: request}, nil
	})}
	result := executeRequest(t.Context(), client, Config{URL: "https://benchmark.example.test/asset?fixed=yes", Protocol: ProtocolH1, ExpectedStatus: http.StatusOK, RequestHeaders: map[string]string{"Range": "bytes=0-1023"}, UniqueQuery: true, UniqueQueryNamespace: "test-r2"})
	if result.err != nil {
		t.Fatal(result.err)
	}
}

func TestExecuteRequestCanReuseBoundedUniqueQuerySet(t *testing.T) {
	var seen []string
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		seen = append(seen, request.URL.Query().Get("_bench"))
		return &http.Response{StatusCode: http.StatusOK, Proto: "HTTP/1.1", Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok")), Request: request}, nil
	})}
	sequence := new(atomic.Uint64)
	config := Config{Method: http.MethodGet, URL: "https://benchmark.example.test/asset", Protocol: ProtocolH1, ExpectedStatus: http.StatusOK, UniqueQuery: true, UniqueQueryCardinality: 2}
	for range 3 {
		if result := executeRequest(t.Context(), client, config, sequence); result.err != nil {
			t.Fatal(result.err)
		}
	}
	if strings.Join(seen, ",") != "1,2,1" {
		t.Fatalf("bounded query sequence=%v", seen)
	}
}

func TestExecuteRequestAcceptsOnlyConfiguredHeaderValues(t *testing.T) {
	cacheState := "STALE"
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Proto: "HTTP/1.1", Header: http.Header{"X-Cache": {cacheState}}, Body: io.NopCloser(strings.NewReader("ok")), Request: request}, nil
	})}
	config := Config{URL: "https://benchmark.example.test", Protocol: ProtocolH1, ExpectedStatus: http.StatusOK, AllowedHeaders: map[string][]string{"X-Cache": {"HIT", "STALE"}}}
	if result := executeRequest(t.Context(), client, config); result.err != nil {
		t.Fatalf("allowed stale response rejected: %v", result.err)
	}
	cacheState = "MISS"
	if result := executeRequest(t.Context(), client, config); result.err == nil {
		t.Fatal("disallowed cache miss was accepted")
	}
}

func TestExecuteRequestLimitsUnexpectedStatusBody(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusBadGateway, Proto: "HTTP/1.1", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(strings.Repeat("x", maxFailureBodyBytes*2))), Request: request}, nil
	})}
	result := executeRequest(t.Context(), client, Config{URL: "https://benchmark.example.test", Protocol: ProtocolH1, ExpectedStatus: http.StatusOK})
	if result.err == nil || len(result.err.Error()) > maxFailureBodyBytes+128 {
		t.Fatalf("unexpected status diagnostic was not bounded: %v", result.err)
	}
}

func TestH3NewConnectionTransportUsesSharedQUICDialer(t *testing.T) {
	shared := new(quic.Transport)
	roundTripper, _, err := newTransport(Config{Protocol: ProtocolH3}, shared)
	if err != nil {
		t.Fatal(err)
	}
	transport := roundTripper.(*http3.Transport)
	if transport.Dial == nil {
		t.Fatal("H3 transport did not use the shared QUIC transport")
	}
}

func TestCounterDeltaHandlesReset(t *testing.T) {
	if got := counterDelta(15, 10); got != 5 {
		t.Fatalf("counterDelta()=%d, want 5", got)
	}
	if got := counterDelta(3, 10); got != 3 {
		t.Fatalf("counterDelta() after reset=%d, want 3", got)
	}
}

func TestIgnoreMeasurementCancellationRecognizesOnlyLocalH3Cancellation(t *testing.T) {
	ended, cancel := context.WithCancel(t.Context())
	cancel()
	local := &quic.StreamError{ErrorCode: quic.StreamErrorCode(http3.ErrCodeRequestCanceled)}
	if !ignoreMeasurementCancellation(ended, local) {
		t.Fatal("local H3 cancellation after measurement was counted")
	}
	remote := &quic.StreamError{ErrorCode: quic.StreamErrorCode(http3.ErrCodeRequestCanceled), Remote: true}
	if ignoreMeasurementCancellation(ended, remote) {
		t.Fatal("remote H3 cancellation was ignored")
	}
	if ignoreMeasurementCancellation(t.Context(), local) {
		t.Fatal("H3 cancellation during measurement was ignored")
	}
}

func TestRunStateMetrics(t *testing.T) {
	state := &runState{protocols: make(map[string]uint64)}
	state.record(requestResult{latency: time.Millisecond, ttfb: 500 * time.Microsecond, bytes: 1024, protocol: "HTTP/3.0"})
	state.record(requestResult{latency: 3 * time.Millisecond, ttfb: time.Millisecond, bytes: 1024, protocol: "HTTP/3.0"})
	metrics, failures, errorCounts := state.metrics(time.Second)
	if len(failures) != 0 || metrics.RPS != 2 || metrics.P50MS != 1 || metrics.P99MS != 3 || metrics.NegotiatedProtocol != "HTTP/3.0" {
		t.Fatalf("metrics=%+v failures=%v", metrics, failures)
	}
	if len(errorCounts) != 0 {
		t.Fatalf("error counts=%v", errorCounts)
	}
}

func TestRunWorkersDrainsInflightRequestAfterDispatchDeadline(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		time.Sleep(25 * time.Millisecond)
		if request.Context().Err() != nil {
			return nil, request.Context().Err()
		}
		return &http.Response{StatusCode: http.StatusOK, Proto: "HTTP/1.1", Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok")), Request: request}, nil
	})}
	dispatchContext, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	state := &runState{protocols: make(map[string]uint64), headers: make(map[string]map[string]uint64)}
	runWorkersWithRequestContext(dispatchContext, t.Context(), Config{URL: "https://benchmark.example.test", Protocol: ProtocolH1, ExpectedStatus: http.StatusOK, Concurrency: 1}, client, state, nil)
	metrics, failures, _ := state.metrics(10 * time.Millisecond)
	if metrics.Successes != 1 || metrics.Failures != 0 || len(failures) != 0 {
		t.Fatalf("metrics=%+v failures=%v", metrics, failures)
	}
}

func TestRunStateClassifiesAllFailuresBeyondSamples(t *testing.T) {
	state := &runState{}
	for range 25 {
		state.recordFailure(errors.New("status 502, want 200"))
	}
	metrics, samples, counts := state.metrics(time.Second)
	if metrics.Failures != 25 || len(samples) != 20 || counts["http_5xx"] != 25 {
		t.Fatalf("metrics=%+v samples=%d counts=%v", metrics, len(samples), counts)
	}
}
