package agentbench

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
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

func TestRunStateMetrics(t *testing.T) {
	state := &runState{protocols: make(map[string]uint64)}
	state.record(requestResult{latency: time.Millisecond, ttfb: 500 * time.Microsecond, bytes: 1024, protocol: "HTTP/3.0"})
	state.record(requestResult{latency: 3 * time.Millisecond, ttfb: time.Millisecond, bytes: 1024, protocol: "HTTP/3.0"})
	metrics, failures := state.metrics(time.Second)
	if len(failures) != 0 || metrics.RPS != 2 || metrics.P50MS != 1 || metrics.P99MS != 3 || metrics.NegotiatedProtocol != "HTTP/3.0" {
		t.Fatalf("metrics=%+v failures=%v", metrics, failures)
	}
}
