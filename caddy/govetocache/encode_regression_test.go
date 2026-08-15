package govetocache

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"go.uber.org/zap"

	"goveto-edge/caddy/simplefs"
)

// proxyLikeNext mimics the production path between the cache handler and the
// origin: a real HTTP transport that delivers the body as ~32 KiB writes at
// memory speed, which is what saturates a small encode queue.
func proxyLikeNext(origin *httptest.Server) caddyhttp.Handler {
	client := origin.Client()
	return caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		req, err := http.NewRequestWithContext(r.Context(), r.Method, origin.URL+r.URL.RequestURI(), nil)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return err
		}
		defer resp.Body.Close()
		transferHeader(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		buffer := make([]byte, 32*1024)
		_, err = io.CopyBuffer(w, resp.Body, buffer)
		return err
	})
}

// Regression coverage for reverse-proxy bodies arriving as ~32 KiB writes.
func TestStreamEncodeCommitsUnderTransportPacedWrites(t *testing.T) {
	body := bytes.Repeat([]byte{0}, 1<<20)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Cache-Control", "public, max-age=300, stale-while-revalidate=60, stale-if-error=60")
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	defer origin.Close()

	dir := t.TempDir()
	t.Cleanup(simplefs.OverrideDiskUsageForTesting(dir, 1<<40, 0))
	storage, err := simplefs.Acquire(simplefs.Config{Path: dir, MaxSizeBytes: 64 << 20}, zap.NewNop().Sugar())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })

	handler := &Handler{SiteID: "site", Path: dir, storage: storage, DefaultTTL: 60, XCache: true}
	next := proxyLikeNext(origin)
	request := func() *http.Request {
		req := httptest.NewRequest(http.MethodGet, "http://example.test/bytes/1048576?cache_bench=hot-run-1024", nil)
		req.Host = "example.test"
		return req
	}

	recorder := httptest.NewRecorder()
	if err := handler.ServeHTTP(recorder, request(), next); err != nil {
		t.Fatal(err)
	}
	if cache := recorder.Header().Get("X-Cache"); cache != "MISS" {
		t.Fatalf("warmup cache=%q", cache)
	}
	if recorder.Body.Len() != len(body) || !bytes.Equal(recorder.Body.Bytes(), body) {
		t.Fatalf("warmup body len=%d", recorder.Body.Len())
	}

	// The measured c32 burst must read the committed object back, not restart
	// the origin fetch on every request.
	var wg sync.WaitGroup
	results := make(chan string, 32)
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recorder := newSyncRecorder()
			if err := handler.ServeHTTP(recorder, request(), next); err != nil {
				results <- "err:" + err.Error()
				return
			}
			results <- fmt.Sprintf("%s:%d", recorder.header.Get("X-Cache"), recorder.buf.Len())
		}()
	}
	wg.Wait()
	close(results)
	for result := range results {
		if result != fmt.Sprintf("HIT:%d", len(body)) {
			t.Fatalf("concurrent result=%s, want HIT with the full body", result)
		}
	}
}

func TestStreamEncodeDropsImmediatelyWhenQueueFull(t *testing.T) {
	oldSlots := encodeQueueSlots
	encodeQueueSlots = 1
	t.Cleanup(func() { encodeQueueSlots = oldSlots })

	storage, dir := newTestCache(t)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releasePut := func() { releaseOnce.Do(func() { close(release) }) }
	defer releasePut()
	queueDrops := 0
	sess := startEncodeSession(func(source io.Reader) error {
		<-release
		return storage.PutReader("base", "varied", source, 128<<10, nil, nil, "", time.Minute, "real")
	}, []byte("header"), func() { queueDrops++ })
	sess.tryWrite([]byte("first"))
	deadline := time.Now().Add(time.Second)
	for len(sess.chunks) != 0 && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if len(sess.chunks) != 0 {
		t.Fatal("drainer did not take the first chunk")
	}
	sess.tryWrite([]byte("queued"))
	if len(sess.chunks) != 1 {
		t.Fatalf("queue depth=%d, want 1", len(sess.chunks))
	}
	started := time.Now()
	sess.tryWrite(make([]byte, 64*1024))
	sess.tryWrite([]byte("ignored after drop"))
	if queueDrops != 1 {
		t.Fatalf("queue drop metric=%d, want 1", queueDrops)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("full queue blocked the origin write for %v", elapsed)
	}
	finished := time.Now()
	if err := sess.finish(); err == nil {
		t.Fatal("finish must report the drop")
	}
	if elapsed := time.Since(finished); elapsed > time.Second {
		t.Fatalf("finish blocked for %v after the encoder stalled", elapsed)
	}
	releasePut()
	sess.abandon()
	fresh, stale, _ := storage.LookupEntry("base", &http.Request{Method: http.MethodGet, Header: http.Header{}})
	if fresh != nil || stale != nil {
		if fresh != nil {
			_ = fresh.Body.Close()
		}
		if stale != nil {
			_ = stale.Body.Close()
		}
		t.Fatal("dropped stream published a cache object")
	}
	assertNoBodyObjects(t, dir)
}

func TestStreamEncodeWaitsForCommitAfterDrain(t *testing.T) {
	release := make(chan struct{})
	commitStarted := make(chan struct{})
	sess := startEncodeSession(func(source io.Reader) error {
		if _, err := io.Copy(io.Discard, source); err != nil {
			return err
		}
		close(commitStarted)
		<-release // the body is consumed, but the final storage commit hangs
		return nil
	}, []byte("header"), nil)
	sess.tryWrite([]byte("body"))

	finished := make(chan error, 1)
	go func() { finished <- sess.finish() }()
	select {
	case <-commitStarted:
	case <-time.After(time.Second):
		t.Fatal("storage commit did not start")
	}

	select {
	case err := <-finished:
		t.Fatalf("finish returned during a valid slow commit: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("finish error after commit release: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("finish did not return after commit completed")
	}
}

func TestEncodeAbandonReturnsAfterCleanupTimeout(t *testing.T) {
	oldTimeout := encodeCleanupTimeout
	encodeCleanupTimeout = 50 * time.Millisecond
	t.Cleanup(func() { encodeCleanupTimeout = oldTimeout })

	putStarted := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releasePut := func() { releaseOnce.Do(func() { close(release) }) }
	defer releasePut()
	sess := startEncodeSession(func(io.Reader) error {
		close(putStarted)
		<-release
		return nil
	}, nil, nil)
	<-putStarted

	returned := make(chan struct{})
	go func() {
		sess.abandon()
		close(returned)
	}()
	select {
	case <-returned:
		t.Fatal("abandon returned before the cleanup timeout")
	case <-time.After(encodeCleanupTimeout / 2):
	}
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("abandon did not return after the cleanup timeout")
	}
	releasePut()
	select {
	case <-sess.done:
	case <-time.After(time.Second):
		t.Fatal("put did not exit after release")
	}
}

func TestEncodeAbandonReportsPipeErrorAfterCompleteBody(t *testing.T) {
	header := []byte("header")
	body := []byte("body")
	bodyRead := make(chan struct{})
	putErr := make(chan error, 1)
	sess := startEncodeSession(func(source io.Reader) error {
		content := make([]byte, len(header)+len(body))
		if _, err := io.ReadFull(source, content); err != nil {
			putErr <- err
			return err
		}
		close(bodyRead)
		var extra [1]byte
		_, err := source.Read(extra[:])
		putErr <- err
		return err
	}, header, nil)
	sess.tryWrite(body)
	select {
	case <-bodyRead:
	case <-time.After(time.Second):
		t.Fatal("put did not consume the complete body")
	}
	sess.abandon()
	if err := <-putErr; !errors.Is(err, errEncodeDropped) {
		t.Fatalf("put error=%v, want %v", err, errEncodeDropped)
	}
}

func TestEncodeFinishIsIdempotent(t *testing.T) {
	sess := startEncodeSession(func(source io.Reader) error {
		_, err := io.Copy(io.Discard, source)
		return err
	}, []byte("header"), nil)
	sess.tryWrite([]byte("body"))
	if err := sess.finish(); err != nil {
		t.Fatal(err)
	}
	if err := sess.finish(); err != nil {
		t.Fatalf("second finish: %v", err)
	}
}

type syncRecorder struct {
	mu     sync.Mutex
	header http.Header
	buf    bytes.Buffer
}

func newSyncRecorder() *syncRecorder        { return &syncRecorder{header: http.Header{}} }
func (c *syncRecorder) Header() http.Header { return c.header }
func (c *syncRecorder) WriteHeader(int)     {}
func (c *syncRecorder) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}
