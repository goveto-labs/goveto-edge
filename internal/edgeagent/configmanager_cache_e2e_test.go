package edgeagent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/darkweak/souin/plugins/caddy"

	_ "goveto-edge/caddy/cacheheaders"
	_ "goveto-edge/caddy/cachematch"
	_ "goveto-edge/caddy/cachepurge"
	cachefs "goveto-edge/caddy/simplefs"
	_ "goveto-edge/caddy/waf"
	"goveto-edge/internal/edgeprotocol"
	cachepolicy "goveto-edge/internal/policy"
)

type originCounters struct {
	mu     sync.Mutex
	values map[string]int
}

func (c *originCounters) increment(key string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.values[key]++
	return c.values[key]
}

func (c *originCounters) count(key string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.values[key]
}

func TestAgentCacheEndToEnd(t *testing.T) {
	ensureAgentLogSink(t)
	counters := &originCounters{values: map[string]int{}}
	var staleOriginFailure atomic.Bool
	var swrOriginDelay atomic.Bool
	var noCacheConditional atomic.Int32
	video := bytes.Repeat([]byte("0123456789abcdef"), 128<<10)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path + " " + r.Header.Get("X-Variant")
		count := counters.increment(key)
		switch r.URL.Path {
		case "/coalesced":
			time.Sleep(150 * time.Millisecond)
		case "/tagged":
			w.Header().Set("Surrogate-Key", "group-a")
		case "/no-store":
			w.Header().Set("Cache-Control", "no-store")
		case "/private":
			w.Header().Set("Cache-Control", "private, max-age=120")
		case "/no-cache", "/must-revalidate":
			w.Header().Set("ETag", `"v1"`)
			if r.URL.Path == "/no-cache" {
				w.Header().Set("Cache-Control", "public, max-age=120, no-cache")
			} else {
				w.Header().Set("Cache-Control", "public, max-age=1, must-revalidate")
			}
			if r.Header.Get("If-None-Match") == `"v1"` {
				if r.URL.Path == "/no-cache" {
					noCacheConditional.Add(1)
				}
				w.WriteHeader(http.StatusNotModified)
				return
			}
		case "/cookie":
			w.Header().Set("Cache-Control", "public, max-age=120")
			w.Header().Set("Set-Cookie", "session=secret; Path=/")
		case "/status/404":
			w.WriteHeader(http.StatusNotFound)
		case "/stale", "/stale-500", "/stale-503", "/stale-504", "/stale-non-error", "/stale-interrupted":
			if staleOriginFailure.Load() {
				switch r.URL.Path {
				case "/stale-500":
					w.WriteHeader(http.StatusInternalServerError)
				case "/stale-503":
					w.WriteHeader(http.StatusServiceUnavailable)
				case "/stale-504":
					w.WriteHeader(http.StatusGatewayTimeout)
				case "/stale-non-error":
					w.WriteHeader(http.StatusNotFound)
				case "/stale-interrupted":
					connection, _, err := w.(http.Hijacker).Hijack()
					if err == nil {
						_, _ = connection.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 20\r\nCache-Control: public, max-age=1\r\n\r\nbad"))
						_ = connection.Close()
					}
					return
				default:
					w.WriteHeader(http.StatusBadGateway)
				}
			}
		case "/swr":
			w.Header().Set("Cache-Control", "public, max-age=1, stale-while-revalidate=300")
			if swrOriginDelay.Load() {
				time.Sleep(300 * time.Millisecond)
			}
		case "/spoof-internal":
			w.Header().Set("X-Goveto-Origin-Content-Length", "999")
			w.Header().Set("X-Goveto-Origin-Method", http.MethodPost)
		case "/video":
			http.ServeContent(w, r, "video.mp4", time.Unix(1, 0), bytes.NewReader(video))
			return
		case "/interrupted":
			if count == 1 {
				hijacker, ok := w.(http.Hijacker)
				if !ok {
					http.Error(w, "hijacking unavailable", http.StatusInternalServerError)
					return
				}
				connection, _, err := hijacker.Hijack()
				if err != nil {
					return
				}
				_, _ = connection.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 20\r\nCache-Control: public, max-age=120\r\n\r\nbad"))
				_ = connection.Close()
				return
			}
		}
		_, _ = fmt.Fprintf(w, "%s:%d:%s", r.URL.Path, count, r.Header.Get("X-Variant"))
	}))
	defer origin.Close()

	port := freePort(t)
	cacheDirectory := filepath.Join(t.TempDir(), "cache")
	t.Cleanup(cachefs.OverrideDiskUsageForTesting(cacheDirectory, 1<<40, 0))
	manager := NewConfigManager(filepath.Join(t.TempDir(), "sites.json"), ":"+strconv.Itoa(port))
	manager.nodeConfig = NodeConfig{
		CacheDirectory:      cacheDirectory,
		MaxSizeBytes:        64 << 20,
		MaxDiskUsagePercent: 90,
	}
	config := SiteConfig{
		SiteID:   "cache-site",
		Version:  1,
		Domains:  []string{"cache.example.test"},
		Listener: ListenerConfig{HTTPEnabled: true, HTTPPort: port},
		Origins:  []OriginConfig{{Protocol: "http", Address: strings.TrimPrefix(origin.URL, "http://")}},
	}
	cache := cachepolicy.DefaultCachePolicy()
	cache.Enabled = true
	cache.TTL.DefaultSeconds = 120
	cache.VaryHeaders = []string{"X-Variant"}
	config.Cache = toMap(t, cache)
	if err := manager.ApplySite(config); err != nil {
		t.Fatalf("apply cache site: %v", err)
	}
	defer manager.Stop()

	t.Run("hit miss vary status and bypass semantics", func(t *testing.T) {
		first := requestEdge(t, port, config.Domains[0], http.MethodGet, "/asset.css", nil)
		second := requestEdge(t, port, config.Domains[0], http.MethodGet, "/asset.css", nil)
		if first.body != second.body || counters.count("GET /asset.css ") != 1 {
			t.Fatalf("GET was not cached: first=%q second=%q count=%d stats=%#v", first.body, second.body, counters.count("GET /asset.css "), cachefs.Stats(manager.nodeConfig.CacheDirectory))
		}
		if first.header.Get("X-Cache") != "MISS" || second.header.Get("X-Cache") != "HIT" {
			t.Fatalf("unexpected cache headers: first=%q second=%q", first.header.Get("X-Cache"), second.header.Get("X-Cache"))
		}

		requestEdge(t, port, config.Domains[0], http.MethodGet, "/vary", http.Header{"X-Variant": {"a"}})
		requestEdge(t, port, config.Domains[0], http.MethodGet, "/vary", http.Header{"X-Variant": {"b"}})
		requestEdge(t, port, config.Domains[0], http.MethodGet, "/vary", http.Header{"X-Variant": {"a"}})
		if counters.count("GET /vary a") != 1 || counters.count("GET /vary b") != 1 {
			t.Fatalf("vary header cache key failed: %#v", counters.values)
		}

		requestEdge(t, port, config.Domains[0], http.MethodGet, "/status/404", nil)
		response404 := requestEdge(t, port, config.Domains[0], http.MethodGet, "/status/404", nil)
		if response404.status != http.StatusNotFound || counters.count("GET /status/404 ") != 1 {
			t.Fatalf("configured 404 TTL was not cached: status=%d count=%d", response404.status, counters.count("GET /status/404 "))
		}

		requestEdge(t, port, config.Domains[0], http.MethodPost, "/post", nil)
		requestEdge(t, port, config.Domains[0], http.MethodPost, "/post", nil)
		if counters.count("POST /post ") != 2 {
			t.Fatalf("POST should bypass cache, count=%d", counters.count("POST /post "))
		}

		requestEdge(t, port, config.Domains[0], http.MethodGet, "/no-store", nil)
		requestEdge(t, port, config.Domains[0], http.MethodGet, "/no-store", nil)
		if counters.count("GET /no-store ") != 2 {
			t.Fatalf("Cache-Control no-store response was cached")
		}

		requestEdge(t, port, config.Domains[0], http.MethodGet, "/request-no-store", http.Header{"Cache-Control": {"no-store"}})
		requestEdge(t, port, config.Domains[0], http.MethodGet, "/request-no-store", nil)
		requestEdge(t, port, config.Domains[0], http.MethodGet, "/request-no-store", nil)
		if counters.count("GET /request-no-store ") != 2 {
			t.Fatal("request Cache-Control no-store response changed shared storage")
		}

		pragma := requestEdge(t, port, config.Domains[0], http.MethodGet, "/asset.css", http.Header{"Pragma": {"no-cache"}})
		if pragma.body == second.body || counters.count("GET /asset.css ") != 2 {
			t.Fatal("legacy Pragma no-cache did not revalidate the cached response")
		}

		spoofed := requestEdge(t, port, config.Domains[0], http.MethodPost, "/spoof-internal", nil)
		if spoofed.header.Get("X-Goveto-Origin-Content-Length") != "" || spoofed.header.Get("X-Goveto-Origin-Method") != "" {
			t.Fatalf("internal validation headers leaked on bypass response: %v", spoofed.header)
		}

		requestEdge(t, port, config.Domains[0], http.MethodGet, "/cookie", nil)
		requestEdge(t, port, config.Domains[0], http.MethodGet, "/cookie", nil)
		if counters.count("GET /cookie ") != 2 {
			t.Fatalf("Set-Cookie response was cached")
		}

		requestEdge(t, port, config.Domains[0], http.MethodGet, "/authorized", http.Header{"Authorization": {"Bearer secret"}})
		requestEdge(t, port, config.Domains[0], http.MethodGet, "/authorized", http.Header{"Authorization": {"Bearer secret"}})
		if counters.count("GET /authorized ") != 2 {
			t.Fatalf("authorized request was cached")
		}

		for _, path := range []string{"/private"} {
			requestEdge(t, port, config.Domains[0], http.MethodGet, path, nil)
			requestEdge(t, port, config.Domains[0], http.MethodGet, path, nil)
			if counters.count("GET "+path+" ") != 2 {
				t.Fatalf("%s response was cached", path)
			}
		}

		noCacheFirst := requestEdge(t, port, config.Domains[0], http.MethodGet, "/no-cache", nil)
		noCache := requestEdge(t, port, config.Domains[0], http.MethodGet, "/no-cache", nil)
		if counters.count("GET /no-cache ") != 2 || noCacheConditional.Load() != 0 ||
			noCache.status != http.StatusOK || noCache.body == noCacheFirst.body {
			t.Fatalf("no-cache response was reused: status=%d count=%d conditional=%d body=%q", noCache.status, counters.count("GET /no-cache "), noCacheConditional.Load(), noCache.body)
		}

		mustFirst := requestEdge(t, port, config.Domains[0], http.MethodGet, "/must-revalidate", nil)
		mustFresh := requestEdge(t, port, config.Domains[0], http.MethodGet, "/must-revalidate", nil)
		if counters.count("GET /must-revalidate ") != 2 || mustFresh.status != http.StatusOK ||
			mustFresh.body == mustFirst.body {
			t.Fatal("must-revalidate response was reused")
		}
		time.Sleep(2100 * time.Millisecond)
		mustStale := requestEdge(t, port, config.Domains[0], http.MethodGet, "/must-revalidate", nil)
		if counters.count("GET /must-revalidate ") != 3 || mustStale.status != http.StatusOK ||
			mustStale.body == mustFresh.body {
			t.Fatalf("must-revalidate response was reused after expiry: status=%d count=%d body=%q", mustStale.status, counters.count("GET /must-revalidate "), mustStale.body)
		}
	})

	t.Run("cold requests are coalesced", func(t *testing.T) {
		const parallel = 24
		start := make(chan struct{})
		errors := make(chan error, parallel)
		var wait sync.WaitGroup
		for range parallel {
			wait.Add(1)
			go func() {
				defer wait.Done()
				<-start
				request, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+"/coalesced", nil)
				if err != nil {
					errors <- err
					return
				}
				request.Host = config.Domains[0]
				response, err := http.DefaultClient.Do(request)
				if err == nil {
					_, err = io.Copy(io.Discard, response.Body)
					_ = response.Body.Close()
				}
				if err != nil {
					errors <- err
				}
			}()
		}
		close(start)
		wait.Wait()
		close(errors)
		for err := range errors {
			t.Fatal(err)
		}
		if count := counters.count("GET /coalesced "); count != 1 {
			t.Fatalf("coalesced origin requests = %d, want 1", count)
		}
	})

	t.Run("range large objects and interrupted upstream", func(t *testing.T) {
		headers := http.Header{"Range": {"bytes=100-199"}}
		first := requestEdge(t, port, config.Domains[0], http.MethodGet, "/video", headers)
		second := requestEdge(t, port, config.Domains[0], http.MethodGet, "/video", headers)
		if first.status != http.StatusPartialContent || first.body != string(video[100:200]) || second.body != first.body {
			t.Fatalf("range response mismatch: first=%d/%d second=%d", first.status, len(first.body), len(second.body))
		}
		if first.header.Get("Content-Range") != "bytes 100-199/2097152" || counters.count("GET /video ") != 1 {
			t.Fatalf("range cache mismatch: content-range=%q origins=%d", first.header.Get("Content-Range"), counters.count("GET /video "))
		}
		other := requestEdge(t, port, config.Domains[0], http.MethodGet, "/video", http.Header{"Range": {"bytes=300-399"}})
		if other.body != string(video[300:400]) || counters.count("GET /video ") != 2 {
			t.Fatal("distinct byte ranges shared a cache entry")
		}

		beforeOpen := counters.count("GET /video ")
		for range 2 {
			open := requestEdge(t, port, config.Domains[0], http.MethodGet, "/video", http.Header{"Range": {"bytes=2097052-"}})
			if open.body != string(video[len(video)-100:]) || open.header.Get("X-Cache") != "" {
				t.Fatalf("open-ended range bypass mismatch: status=%d body=%d range=%q x-cache=%q", open.status, len(open.body), open.header.Get("Content-Range"), open.header.Get("X-Cache"))
			}
		}
		if got := counters.count("GET /video "); got != beforeOpen+2 {
			t.Fatalf("open-ended ranges should bypass cache: origins=%d want=%d", got, beforeOpen+2)
		}

		beforeSuffix := counters.count("GET /video ")
		for range 2 {
			suffix := requestEdge(t, port, config.Domains[0], http.MethodGet, "/video", http.Header{"Range": {"bytes=-100"}})
			if suffix.body != string(video[len(video)-100:]) || suffix.header.Get("X-Cache") != "" {
				t.Fatalf("suffix range bypass mismatch: body=%d x-cache=%q", len(suffix.body), suffix.header.Get("X-Cache"))
			}
		}
		if got := counters.count("GET /video "); got != beforeSuffix+2 {
			t.Fatalf("suffix ranges should bypass cache: origins=%d want=%d", got, beforeSuffix+2)
		}

		beforeInvalid := counters.count("GET /video ")
		for range 2 {
			invalid := requestEdge(t, port, config.Domains[0], http.MethodGet, "/video", http.Header{"Range": {"bytes=3000000-3000100"}})
			if invalid.status != http.StatusRequestedRangeNotSatisfiable {
				t.Fatalf("unsatisfiable range status=%d", invalid.status)
			}
		}
		if got := counters.count("GET /video "); got != beforeInvalid+2 {
			t.Fatalf("416 response was cached: origins=%d want=%d", got, beforeInvalid+2)
		}

		beforeIfRange := counters.count("GET /video ")
		mismatchHeaders := http.Header{"Range": {"bytes=600-699"}, "If-Range": {"Wed, 21 Oct 2015 07:28:00 GMT"}}
		mismatch := requestEdge(t, port, config.Domains[0], http.MethodGet, "/video", mismatchHeaders)
		mismatchHit := requestEdge(t, port, config.Domains[0], http.MethodGet, "/video", mismatchHeaders)
		if mismatch.status != http.StatusOK || mismatch.body != string(video) || mismatchHit.body != mismatch.body || mismatch.header.Get("X-Cache") != "" {
			t.Fatal("mismatched If-Range response was not isolated")
		}
		matchHeaders := http.Header{"Range": {"bytes=700-799"}, "If-Range": {first.header.Get("Last-Modified")}}
		matched := requestEdge(t, port, config.Domains[0], http.MethodGet, "/video", matchHeaders)
		matchedHit := requestEdge(t, port, config.Domains[0], http.MethodGet, "/video", matchHeaders)
		if matched.status != http.StatusPartialContent || matched.body != string(video[700:800]) || matchedHit.body != matched.body || matched.header.Get("X-Cache") != "" {
			t.Fatal("matching If-Range response was not cached separately")
		}
		if got := counters.count("GET /video "); got != beforeIfRange+4 {
			t.Fatalf("If-Range bypass used %d origins, want %d", got, beforeIfRange+4)
		}

		beforeFull := counters.count("GET /video ")
		full := requestEdge(t, port, config.Domains[0], http.MethodGet, "/video", nil)
		fullHit := requestEdge(t, port, config.Domains[0], http.MethodGet, "/video", nil)
		if len(full.body) != len(video) || fullHit.header.Get("X-Cache") != "HIT" || counters.count("GET /video ") != beforeFull+1 {
			t.Fatalf("large full object was not cached: size=%d x-cache=%q origins=%d", len(full.body), fullHit.header.Get("X-Cache"), counters.count("GET /video "))
		}

		config.Version++
		cache.CacheRangeRequests = false
		config.Cache = toMap(t, cache)
		if err := manager.ApplySite(config); err != nil {
			t.Fatal(err)
		}
		beforeBypass := counters.count("GET /video ")
		for range 2 {
			bypassed := requestEdge(t, port, config.Domains[0], http.MethodGet, "/video", http.Header{"Range": {"bytes=500-599"}})
			if bypassed.status != http.StatusPartialContent || bypassed.body != string(video[500:600]) || bypassed.header.Get("X-Cache") != "" {
				t.Fatalf("disabled range cache did not bypass: status=%d body=%d x-cache=%q", bypassed.status, len(bypassed.body), bypassed.header.Get("X-Cache"))
			}
		}
		if got := counters.count("GET /video "); got != beforeBypass+2 {
			t.Fatalf("disabled range cache origin requests=%d, want %d", got, beforeBypass+2)
		}
		config.Version++
		cache.CacheRangeRequests = true
		config.Cache = toMap(t, cache)
		if err := manager.ApplySite(config); err != nil {
			t.Fatal(err)
		}

		requestEdgeExpectReadError(t, port, config.Domains[0], "/interrupted")
		complete := requestEdge(t, port, config.Domains[0], http.MethodGet, "/interrupted", nil)
		hit := requestEdge(t, port, config.Domains[0], http.MethodGet, "/interrupted", nil)
		if complete.body != "/interrupted:2:" || hit.body != complete.body || counters.count("GET /interrupted ") != 2 {
			t.Fatalf("partial response polluted cache: complete=%q hit=%q origins=%d", complete.body, hit.body, counters.count("GET /interrupted "))
		}
	})

	t.Run("URL prefix tag and all purge change actual cache contents", func(t *testing.T) {
		primeCache(t, counters, port, config.Domains[0], "/purge/url")
		result, err := manager.PurgeDetailed(edgeprotocol.PurgeRequest{SiteID: config.SiteID, Type: "URL", Values: []string{"/purge/url"}})
		if err != nil {
			t.Fatal(err)
		}
		if result.Objects != 1 {
			t.Fatalf("URL purge objects=%d, want 1", result.Objects)
		}
		requestEdge(t, port, config.Domains[0], http.MethodGet, "/purge/url", nil)
		if counters.count("GET /purge/url ") != 2 {
			t.Fatalf("URL purge did not evict cached response")
		}

		primeCache(t, counters, port, config.Domains[0], "/prefix/a")
		primeCache(t, counters, port, config.Domains[0], "/prefix/b")
		result, err = manager.PurgeDetailed(edgeprotocol.PurgeRequest{SiteID: config.SiteID, Type: "PREFIX", Values: []string{"/prefix/"}})
		if err != nil {
			t.Fatal(err)
		}
		if result.Objects != 2 {
			t.Fatalf("prefix purge objects=%d, want 2", result.Objects)
		}
		requestEdge(t, port, config.Domains[0], http.MethodGet, "/prefix/a", nil)
		requestEdge(t, port, config.Domains[0], http.MethodGet, "/prefix/b", nil)
		if counters.count("GET /prefix/a ") != 2 || counters.count("GET /prefix/b ") != 2 {
			t.Fatalf("prefix purge did not evict both responses")
		}

		primeCache(t, counters, port, config.Domains[0], "/tagged")
		result, err = manager.PurgeDetailed(edgeprotocol.PurgeRequest{SiteID: config.SiteID, Type: "TAG", Values: []string{"group-a"}})
		if err != nil {
			t.Fatal(err)
		}
		if result.Objects != 1 {
			t.Fatalf("tag purge objects=%d, want 1", result.Objects)
		}
		requestEdge(t, port, config.Domains[0], http.MethodGet, "/tagged", nil)
		if counters.count("GET /tagged ") != 2 {
			t.Fatalf("tag purge did not evict tagged response")
		}

		primeCache(t, counters, port, config.Domains[0], "/purge/all")
		cachePath := filepath.Join(manager.nodeConfig.CacheDirectory, config.SiteID)
		beforeAll := cachefs.Stats(cachePath).Entries
		result, err = manager.PurgeDetailed(edgeprotocol.PurgeRequest{SiteID: config.SiteID, Type: "ALL"})
		if err != nil {
			t.Fatal(err)
		}
		if result.Objects != int(beforeAll) || cachefs.Stats(cachePath).Entries != 0 {
			t.Fatalf("all purge objects=%d, want %d; remaining=%d", result.Objects, beforeAll, cachefs.Stats(cachePath).Entries)
		}
		requestEdge(t, port, config.Domains[0], http.MethodGet, "/purge/all", nil)
		if counters.count("GET /purge/all ") != 2 {
			t.Fatalf("all purge did not evict cached response")
		}
	})

	t.Run("external PURGE method evicts the requested URL", func(t *testing.T) {
		config.Version++
		cache.AllowPurgeMethod = true
		config.Cache = toMap(t, cache)
		if err := manager.ApplySite(config); err != nil {
			t.Fatal(err)
		}
		primeCache(t, counters, port, config.Domains[0], "/external-purge")
		response := requestEdge(t, port, config.Domains[0], "PURGE", "/external-purge", nil)
		if response.status != http.StatusNoContent {
			t.Fatalf("PURGE status=%d body=%q", response.status, response.body)
		}
		if response.header.Get("X-Goveto-Purged-Objects") != "1" {
			t.Fatalf("PURGE object count=%q", response.header.Get("X-Goveto-Purged-Objects"))
		}
		requestEdge(t, port, config.Domains[0], http.MethodGet, "/external-purge", nil)
		if counters.count("GET /external-purge ") != 2 {
			t.Fatal("external PURGE did not evict the URL")
		}
	})

	t.Run("stale if error and response header switches", func(t *testing.T) {
		config.Version++
		cache.TTL.DefaultSeconds = 2
		cache.TTL.Status["200"] = 2
		cache.Stale.Enabled = true
		cache.Stale.IfErrorSeconds = 2
		cache.Stale.WhileRevalidateSeconds = 0
		config.Cache = toMap(t, cache)
		if err := manager.ApplySite(config); err != nil {
			t.Fatal(err)
		}
		staleBodies := make(map[string]string)
		staleOriginCounts := make(map[string]int)
		primeStale := func(path string) edgeResponse {
			first := requestEdge(t, port, config.Domains[0], http.MethodGet, path, nil)
			deadline := time.Now().Add(2 * time.Second)
			for {
				cached := requestEdge(t, port, config.Domains[0], http.MethodGet, path, nil)
				if cached.header.Get("X-Cache") == "HIT" {
					staleBodies[path] = cached.body
					staleOriginCounts[path] = counters.count("GET " + path + " ")
					return first
				}
				if time.Now().After(deadline) {
					t.Fatalf("failed to prime %s: x-cache=%q origins=%d", path, cached.header.Get("X-Cache"), counters.count("GET "+path+" "))
				}
				time.Sleep(20 * time.Millisecond)
			}
		}
		first := primeStale("/stale")
		if got := first.header.Get("Cache-Control"); got != "public, max-age=2, stale-if-error=2" {
			t.Fatalf("stale cache control=%q", got)
		}
		for _, path := range []string{"/stale-500", "/stale-503", "/stale-504", "/stale-interrupted", "/stale-non-error"} {
			primeStale(path)
		}
		time.Sleep(2100 * time.Millisecond)
		staleOriginFailure.Store(true)
		for _, path := range []string{"/stale", "/stale-500", "/stale-503", "/stale-504", "/stale-interrupted"} {
			stale := requestEdge(t, port, config.Domains[0], http.MethodGet, path, nil)
			if stale.status != http.StatusOK || stale.body != staleBodies[path] || stale.header.Get("X-Cache") != "STALE" {
				t.Fatalf("%s stale response status=%d body=%q want=%q x-cache=%q stats=%#v", path, stale.status, stale.body, staleBodies[path], stale.header.Get("X-Cache"), cachefs.Stats(cacheDirectory))
			}
			if count := counters.count("GET " + path + " "); count != staleOriginCounts[path]+1 {
				t.Fatalf("%s stale revalidation origin count=%d, want %d", path, count, staleOriginCounts[path]+1)
			}
		}
		nonError := requestEdge(t, port, config.Domains[0], http.MethodGet, "/stale-non-error", nil)
		if nonError.status != http.StatusNotFound || nonError.header.Get("X-Cache") == "STALE" {
			t.Fatalf("non-error response incorrectly used stale: status=%d x-cache=%q", nonError.status, nonError.header.Get("X-Cache"))
		}
		time.Sleep(2100 * time.Millisecond)
		expired := requestEdge(t, port, config.Domains[0], http.MethodGet, "/stale", nil)
		staleOriginFailure.Store(false)
		if expired.status != http.StatusBadGateway || expired.header.Get("X-Cache") == "STALE" {
			t.Fatalf("stale-if-error exceeded window: status=%d x-cache=%q", expired.status, expired.header.Get("X-Cache"))
		}

		config.Version++
		cache.Stale.WhileRevalidateSeconds = 30
		config.Cache = toMap(t, cache)
		if err := manager.ApplySite(config); err != nil {
			t.Fatal(err)
		}
		swrFirst := requestEdge(t, port, config.Domains[0], http.MethodGet, "/swr", nil)
		time.Sleep(2100 * time.Millisecond)
		swrOriginDelay.Store(true)
		started := time.Now()
		swrStale := requestEdge(t, port, config.Domains[0], http.MethodGet, "/swr", nil)
		elapsed := time.Since(started)
		if swrStale.body != swrFirst.body || swrStale.header.Get("X-Cache") != "STALE" || elapsed >= 280*time.Millisecond {
			t.Fatalf("SWR did not return stale immediately: body=%q x-cache=%q elapsed=%s", swrStale.body, swrStale.header.Get("X-Cache"), elapsed)
		}
		deadline := time.Now().Add(2 * time.Second)
		for counters.count("GET /swr ") < 2 && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		swrOriginDelay.Store(false)
		var refreshed edgeResponse
		deadline = time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			refreshed = requestEdge(t, port, config.Domains[0], http.MethodGet, "/swr", nil)
			if refreshed.body != swrFirst.body {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if counters.count("GET /swr ") < 2 || refreshed.body == swrFirst.body {
			t.Fatalf("SWR background refresh failed: first=%q refreshed=%q origins=%d", swrFirst.body, refreshed.body, counters.count("GET /swr "))
		}

		config.Version++
		cache.ResponseHeaders.XCache = false
		cache.ResponseHeaders.Age = false
		config.Cache = toMap(t, cache)
		if err := manager.ApplySite(config); err != nil {
			t.Fatal(err)
		}
		requestEdge(t, port, config.Domains[0], http.MethodGet, "/headers-off", nil)
		headersOff := requestEdge(t, port, config.Domains[0], http.MethodGet, "/headers-off", nil)
		if headersOff.header.Get("X-Cache") != "" || headersOff.header.Get("Age") != "" {
			t.Fatalf("cache response headers were not disabled: %v", headersOff.header)
		}
	})

	t.Run("site cache and all purge are isolated", func(t *testing.T) {
		config.Version++
		cache.TTL.DefaultSeconds = 120
		cache.TTL.Status["200"] = 120
		config.Cache = toMap(t, cache)
		if err := manager.ApplySite(config); err != nil {
			t.Fatal(err)
		}
		secondCounters := &originCounters{values: map[string]int{}}
		secondOrigin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			count := secondCounters.increment(r.Method + " " + r.URL.Path + " ")
			_, _ = fmt.Fprintf(w, "second:%d", count)
		}))
		defer secondOrigin.Close()

		second := config
		second.SiteID = "cache-site-2"
		second.Version = 1
		second.Domains = []string{"cache-2.example.test"}
		second.Origins = []OriginConfig{{Protocol: "http", Address: strings.TrimPrefix(secondOrigin.URL, "http://")}}
		if err := manager.ApplySite(second); err != nil {
			t.Fatal(err)
		}
		primeCache(t, counters, port, config.Domains[0], "/isolation")
		primeCache(t, secondCounters, port, second.Domains[0], "/isolation")
		if err := manager.Purge(context.Background(), edgeprotocol.PurgeRequest{SiteID: config.SiteID, Type: "ALL"}); err != nil {
			t.Fatal(err)
		}
		requestEdge(t, port, config.Domains[0], http.MethodGet, "/isolation", nil)
		requestEdge(t, port, second.Domains[0], http.MethodGet, "/isolation", nil)
		if counters.count("GET /isolation ") != 2 {
			t.Fatal("site one cache was not purged")
		}
		if secondCounters.count("GET /isolation ") != 1 {
			t.Fatal("site two cache was incorrectly purged")
		}
	})

	t.Run("grouped cache expressions control real caching", func(t *testing.T) {
		config.Version++
		cache.Conditions = cachepolicy.CacheConditions{
			GroupOperator: "AND",
			Groups: []cachepolicy.CacheConditionGroup{
				{Operator: "OR", Rules: []cachepolicy.CacheConditionRule{{Type: "EXTENSION", Values: []string{"css"}}, {Type: "PATH_PREFIX", Values: []string{"/assets/"}}}},
				{Operator: "AND", Rules: []cachepolicy.CacheConditionRule{{Type: "PATH_REGEX", Value: `^/assets/`}}},
			},
		}
		config.Cache = toMap(t, cache)
		if err := manager.ApplySite(config); err != nil {
			t.Fatal(err)
		}

		requestEdge(t, port, config.Domains[0], http.MethodGet, "/assets/app.js", nil)
		requestEdge(t, port, config.Domains[0], http.MethodGet, "/assets/app.js", nil)
		if counters.count("GET /assets/app.js ") != 1 {
			t.Fatalf("matching grouped expression did not cache")
		}
		requestEdge(t, port, config.Domains[0], http.MethodGet, "/app.css", nil)
		requestEdge(t, port, config.Domains[0], http.MethodGet, "/app.css", nil)
		if counters.count("GET /app.css ") != 2 {
			t.Fatalf("outer AND expression should bypass cache")
		}
	})
}

type edgeResponse struct {
	status int
	body   string
	header http.Header
}

func requestEdge(t *testing.T, port int, host, method, path string, headers http.Header) edgeResponse {
	t.Helper()
	request, err := http.NewRequest(method, "http://127.0.0.1:"+strconv.Itoa(port)+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = host
	request.Header = headers.Clone()
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return edgeResponse{status: response.StatusCode, body: string(body), header: response.Header.Clone()}
}

func requestEdgeExpectReadError(t *testing.T, port int, host, path string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = host
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(response.Body)
	if readErr == nil && response.StatusCode != http.StatusBadGateway && response.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("interrupted upstream returned status=%d body=%q", response.StatusCode, body)
	}
}

func primeCache(t *testing.T, counters *originCounters, port int, host, path string) {
	t.Helper()
	requestEdge(t, port, host, http.MethodGet, path, nil)
	requestEdge(t, port, host, http.MethodGet, path, nil)
	if count := counters.count("GET " + path + " "); count != 1 {
		t.Fatalf("failed to prime %s, origin count=%d", path, count)
	}
}
