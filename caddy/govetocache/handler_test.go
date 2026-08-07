package govetocache

import (
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"go.uber.org/zap"

	"goveto-edge/caddy/simplefs"
	"goveto-edge/internal/policy"
)

func TestCacheKeyIsUnambiguousAndPreservesRawQuery(t *testing.T) {
	handler := Handler{
		SiteID: "site", KeyParts: []string{
			policy.CacheKeyPartMethod, policy.CacheKeyPartHost, policy.CacheKeyPartPath, policy.CacheKeyPartQuery,
		}, KeyHeaders: []string{"X-Key"},
	}
	first := &http.Request{Method: "GET", Host: "example.test", URL: &url.URL{Path: "/a", RawQuery: "a=1&b=2"}, Header: http.Header{"X-Key": {"bc"}}}
	second := &http.Request{Method: "GET", Host: "example.test", URL: &url.URL{Path: "/a", RawQuery: "b=2&a=1"}, Header: http.Header{"X-Key": {"b", "c"}}}
	if handler.cacheKey(first, nil) == handler.cacheKey(second, nil) {
		t.Fatal("distinct query order or header value boundaries produced the same key")
	}
}

func TestVaryMergesConfiguredHeadersAndIgnoresCompression(t *testing.T) {
	handler := Handler{KeyHeaders: []string{"X-Configured", "Accept-Encoding"}}
	request := &http.Request{Header: http.Header{
		"X-Configured": {"a"}, "X-Origin-Vary": {"b"}, "Accept-Encoding": {"gzip"},
	}}
	varied, ok := handler.variedHeaders(request, http.Header{"Vary": {"X-Origin-Vary, Accept-Encoding"}})
	if !ok || varied.Get("X-Configured") != "a" || varied.Get("X-Origin-Vary") != "b" || varied.Get("Accept-Encoding") != "" {
		t.Fatalf("unexpected varied headers: %#v", varied)
	}
	if _, ok = handler.variedHeaders(request, http.Header{"Vary": {"*"}}); ok {
		t.Fatal("Vary: * was accepted")
	}
}

func TestHiddenHashedKeyIsNotExposed(t *testing.T) {
	handler := Handler{HashKey: true, HideKey: true, XCache: true, Debug: true}
	raw := strings.Repeat("secret", 32)
	if key := handler.storageKey(raw); len(key) != 64 || strings.Contains(key, raw) {
		t.Fatalf("unexpected hashed key %q", key)
	}
	header := http.Header{}
	handler.setResultHeaders(header, "HIT", raw, 30)
	if strings.Contains(header.Get("Cache-Status"), "key=") || header.Get("X-Cache") != "HIT" {
		t.Fatalf("hidden result headers: %#v", header)
	}
}

func TestDebugModeControlsCacheStatus(t *testing.T) {
	raw := "method:GET host:example.test path:/asset"
	off := Handler{XCache: true, Age: true}
	header := http.Header{}
	off.setResultHeaders(header, "HIT", raw, 30)
	if header.Get("Cache-Status") != "" || header.Get("X-Cache") != "HIT" || header.Get("Age") != "0" {
		t.Fatalf("production headers leaked debug status: %#v", header)
	}
	on := Handler{XCache: true, Age: true, Debug: true}
	header = http.Header{}
	on.setResultHeaders(header, "HIT", raw, 30)
	status := header.Get("Cache-Status")
	if !strings.Contains(status, "hit") || !strings.Contains(status, "ttl=30") || !strings.Contains(status, "key=") {
		t.Fatalf("debug Cache-Status missing detail: %q", status)
	}
}

func TestCustomSurrogateHeaderIsStoredWithoutLeakingAlias(t *testing.T) {
	handler := Handler{SurrogateKeyHeader: "X-Cache-Tags"}
	header := http.Header{"X-Cache-Tags": {"one two"}}
	stored := handler.cacheStorageHeader(header)
	if stored.Get("Surrogate-Key") != "one two" || header.Get("Surrogate-Key") != "" {
		t.Fatalf("custom surrogate storage headers: original=%v stored=%v", header, stored)
	}
	groups := surrogateGroups(header, handler.SurrogateKeyHeader)
	if len(groups) != 2 || groups[0] != "one" || groups[1] != "two" {
		t.Fatalf("custom surrogate groups=%v", groups)
	}
}

func FuzzCacheControlParser(f *testing.F) {
	for _, seed := range []string{"public, max-age=60", `private, foo=\"a,b\"`, "no-store", ""} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		parts := splitCacheControl(value)
		if len(parts) == 0 {
			t.Fatal("parser returned no parts")
		}
		_ = hasDirective(value, "no-store")
		_ = replaceDirective(value, "s-maxage", "1")
	})
}

type recordedWrite struct {
	status int
	header http.Header
}

type recordingWriter struct {
	header http.Header
	writes []recordedWrite
}

func (w *recordingWriter) Header() http.Header { return w.header }

func (w *recordingWriter) Write(value []byte) (int, error) { return len(value), nil }

func (w *recordingWriter) WriteHeader(status int) {
	w.writes = append(w.writes, recordedWrite{status: status, header: w.header.Clone()})
}

func TestCapturedResponseForwards1xxToDownstream(t *testing.T) {
	downstream := &recordingWriter{header: http.Header{}}
	captured, err := newCapturedResponse(t.TempDir(), downstream)
	if err != nil {
		t.Fatal(err)
	}
	defer captured.Close()

	// Mirror Caddy's Got1xxResponse hook: copy 1xx headers in, WriteHeader, clear.
	captured.Header().Set("Link", "</style.css>; rel=preload")
	captured.WriteHeader(http.StatusEarlyHints)
	clear(captured.Header())
	if len(downstream.Header()) != 0 {
		t.Fatalf("1xx headers leaked into downstream header map: %#v", downstream.Header())
	}

	captured.Header().Set("Content-Type", "text/html")
	captured.WriteHeader(http.StatusOK)
	if err := captured.WriteResponse(downstream); err != nil {
		t.Fatal(err)
	}

	if len(downstream.writes) != 2 {
		t.Fatalf("downstream writes=%v", downstream.writes)
	}
	if downstream.writes[0].status != http.StatusEarlyHints || downstream.writes[0].header.Get("Link") == "" {
		t.Fatalf("103 not forwarded with its headers: %+v", downstream.writes[0])
	}
	if downstream.writes[1].status != http.StatusOK || downstream.writes[1].header.Get("Content-Type") != "text/html" {
		t.Fatalf("final response not forwarded: %+v", downstream.writes[1])
	}
	if captured.Status() != http.StatusOK {
		t.Fatalf("captured status=%d, want 200", captured.Status())
	}
}

func TestCapturedResponseDrops1xxWithoutDownstream(t *testing.T) {
	captured, err := newCapturedResponse(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer captured.Close()

	captured.WriteHeader(http.StatusEarlyHints)
	captured.WriteHeader(http.StatusOK)

	if captured.Status() != http.StatusOK {
		t.Fatalf("captured status=%d, want 200", captured.Status())
	}
	if captured.wrote1xx {
		t.Fatal("wrote1xx should stay false without downstream")
	}
}

func TestCapturedResponseForwardsMultiple1xx(t *testing.T) {
	downstream := &recordingWriter{header: http.Header{}}
	captured, err := newCapturedResponse(t.TempDir(), downstream)
	if err != nil {
		t.Fatal(err)
	}
	defer captured.Close()

	captured.Header().Set("Link", "</a.css>; rel=preload")
	captured.WriteHeader(http.StatusEarlyHints)
	clear(captured.Header())
	captured.Header().Set("Link", "</b.js>; rel=preload")
	captured.WriteHeader(http.StatusEarlyHints)
	clear(captured.Header())

	captured.Header().Set("Content-Type", "text/html")
	captured.WriteHeader(http.StatusOK)
	if err := captured.WriteResponse(downstream); err != nil {
		t.Fatal(err)
	}

	if !captured.wrote1xx {
		t.Fatal("wrote1xx=false, want true")
	}
	if len(downstream.writes) != 3 {
		t.Fatalf("downstream writes=%v", downstream.writes)
	}
	if downstream.writes[0].status != http.StatusEarlyHints || downstream.writes[0].header.Get("Link") != "</a.css>; rel=preload" {
		t.Fatalf("first 103: %+v", downstream.writes[0])
	}
	if downstream.writes[1].status != http.StatusEarlyHints || downstream.writes[1].header.Get("Link") != "</b.js>; rel=preload" {
		t.Fatalf("second 103: %+v", downstream.writes[1])
	}
	if downstream.writes[2].status != http.StatusOK {
		t.Fatalf("final response: %+v", downstream.writes[2])
	}
}

func TestCapturedResponseLatches101WithoutForwarding(t *testing.T) {
	downstream := &recordingWriter{header: http.Header{}}
	captured, err := newCapturedResponse(t.TempDir(), downstream)
	if err != nil {
		t.Fatal(err)
	}
	defer captured.Close()

	captured.Header().Set("Connection", "Upgrade")
	captured.WriteHeader(http.StatusSwitchingProtocols)

	if captured.Status() != http.StatusSwitchingProtocols {
		t.Fatalf("captured status=%d, want 101", captured.Status())
	}
	if captured.wrote1xx {
		t.Fatal("101 must not be treated as forwarded informational")
	}
	if len(downstream.writes) != 0 {
		t.Fatalf("101 must not be forwarded as interim: %v", downstream.writes)
	}

	// Final-status latch: later WriteHeader is ignored.
	captured.WriteHeader(http.StatusOK)
	if captured.Status() != http.StatusSwitchingProtocols {
		t.Fatalf("captured status=%d after second WriteHeader, want 101", captured.Status())
	}
}

func TestCapturedResponseIgnores1xxAfterFinalStatus(t *testing.T) {
	downstream := &recordingWriter{header: http.Header{}}
	captured, err := newCapturedResponse(t.TempDir(), downstream)
	if err != nil {
		t.Fatal(err)
	}
	defer captured.Close()

	captured.WriteHeader(http.StatusOK)
	captured.Header().Set("Link", "</late.css>; rel=preload")
	captured.WriteHeader(http.StatusEarlyHints)

	if captured.Status() != http.StatusOK {
		t.Fatalf("captured status=%d, want 200", captured.Status())
	}
	if captured.wrote1xx {
		t.Fatal("1xx after final status must not mark wrote1xx")
	}
	if len(downstream.writes) != 0 {
		t.Fatalf("1xx after final status must not reach downstream: %v", downstream.writes)
	}
}

func TestFetchAndServeDoesNotRetryOriginAfter1xxOnWriteError(t *testing.T) {
	storage, err := simplefs.Acquire(simplefs.Config{Path: t.TempDir(), MaxSizeBytes: 1 << 20}, zap.NewNop().Sugar())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })

	var calls atomic.Int32
	next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		calls.Add(1)
		w.Header().Set("Link", "</app.js>; rel=preload")
		w.WriteHeader(http.StatusEarlyHints)
		clear(w.Header())
		// Same-package probe: break the capture file so Write sets writeErr.
		if captured, ok := w.(*capturedResponse); ok {
			_ = captured.file.Close()
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", "4")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("body"))
		return nil
	})

	handler := &Handler{SiteID: "site", Path: t.TempDir(), storage: storage, DefaultTTL: 60}
	downstream := &recordingWriter{header: http.Header{}}
	req := &http.Request{Method: http.MethodGet, Host: "example.test", URL: &url.URL{Path: "/"}, Header: http.Header{}}

	if err := handler.fetchAndServe(downstream, req, next, "raw", "base", nil, simplefs.LookupMetadata{}); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("origin calls=%d, want 1 (no retry after 1xx)", got)
	}
	if len(downstream.writes) != 2 {
		t.Fatalf("downstream writes=%v", downstream.writes)
	}
	if downstream.writes[0].status != http.StatusEarlyHints {
		t.Fatalf("first write=%+v, want 103", downstream.writes[0])
	}
	if downstream.writes[1].status != http.StatusBadGateway {
		t.Fatalf("final write=%+v, want 502", downstream.writes[1])
	}
}

func TestFetchAndServeRetriesOriginOnWriteErrorWithout1xx(t *testing.T) {
	storage, err := simplefs.Acquire(simplefs.Config{Path: t.TempDir(), MaxSizeBytes: 1 << 20}, zap.NewNop().Sugar())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })

	var calls atomic.Int32
	next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		n := calls.Add(1)
		if n == 1 {
			if captured, ok := w.(*capturedResponse); ok {
				_ = captured.file.Close()
			}
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Length", "4")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("body"))
			return nil
		}
		// Retry path writes directly to the real client writer.
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
		return nil
	})

	handler := &Handler{SiteID: "site", Path: t.TempDir(), storage: storage, DefaultTTL: 60}
	downstream := &recordingWriter{header: http.Header{}}
	req := &http.Request{Method: http.MethodGet, Host: "example.test", URL: &url.URL{Path: "/"}, Header: http.Header{}}

	if err := handler.fetchAndServe(downstream, req, next, "raw", "base", nil, simplefs.LookupMetadata{}); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("origin calls=%d, want 2 (retry when no 1xx)", got)
	}
	if len(downstream.writes) != 1 || downstream.writes[0].status != http.StatusOK {
		t.Fatalf("downstream writes=%v", downstream.writes)
	}
}
