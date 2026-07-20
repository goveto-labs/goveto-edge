package edgeagent

import (
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
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path + " " + r.Header.Get("X-Variant")
		count := counters.increment(key)
		switch r.URL.Path {
		case "/tagged":
			w.Header().Set("Surrogate-Key", "group-a")
		case "/no-store":
			w.Header().Set("Cache-Control", "no-store")
		case "/cookie":
			w.Header().Set("Set-Cookie", "session=secret; Path=/")
		case "/status/404":
			w.WriteHeader(http.StatusNotFound)
		case "/stale":
			if staleOriginFailure.Load() {
				w.WriteHeader(http.StatusBadGateway)
			}
		}
		_, _ = fmt.Fprintf(w, "%s:%d:%s", r.URL.Path, count, r.Header.Get("X-Variant"))
	}))
	defer origin.Close()

	port := freePort(t)
	manager := NewConfigManager(filepath.Join(t.TempDir(), "sites.json"), ":"+strconv.Itoa(port))
	manager.SetAgentHost("node-id")
	manager.nodeConfig = NodeConfig{
		CacheDirectory:      filepath.Join(t.TempDir(), "cache"),
		MaxSizeBytes:        64 << 20,
		MaxDiskUsagePercent: 95,
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
			t.Fatalf("GET was not cached: first=%q second=%q count=%d", first.body, second.body, counters.count("GET /asset.css "))
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
	})

	t.Run("URL prefix tag and all purge change actual cache contents", func(t *testing.T) {
		primeCache(t, counters, port, config.Domains[0], "/purge/url")
		if err := manager.Purge(context.Background(), edgeprotocol.PurgeRequest{SiteID: config.SiteID, Type: "URL", Values: []string{"/purge/url"}}); err != nil {
			t.Fatal(err)
		}
		requestEdge(t, port, config.Domains[0], http.MethodGet, "/purge/url", nil)
		if counters.count("GET /purge/url ") != 2 {
			t.Fatalf("URL purge did not evict cached response")
		}

		primeCache(t, counters, port, config.Domains[0], "/prefix/a")
		primeCache(t, counters, port, config.Domains[0], "/prefix/b")
		if err := manager.Purge(context.Background(), edgeprotocol.PurgeRequest{SiteID: config.SiteID, Type: "PREFIX", Values: []string{"/prefix/"}}); err != nil {
			t.Fatal(err)
		}
		requestEdge(t, port, config.Domains[0], http.MethodGet, "/prefix/a", nil)
		requestEdge(t, port, config.Domains[0], http.MethodGet, "/prefix/b", nil)
		if counters.count("GET /prefix/a ") != 2 || counters.count("GET /prefix/b ") != 2 {
			t.Fatalf("prefix purge did not evict both responses")
		}

		primeCache(t, counters, port, config.Domains[0], "/tagged")
		if err := manager.Purge(context.Background(), edgeprotocol.PurgeRequest{SiteID: config.SiteID, Type: "TAG", Values: []string{"group-a"}}); err != nil {
			t.Fatal(err)
		}
		requestEdge(t, port, config.Domains[0], http.MethodGet, "/tagged", nil)
		if counters.count("GET /tagged ") != 2 {
			t.Fatalf("tag purge did not evict tagged response")
		}

		primeCache(t, counters, port, config.Domains[0], "/purge/all")
		if err := manager.Purge(context.Background(), edgeprotocol.PurgeRequest{SiteID: config.SiteID, Type: "ALL"}); err != nil {
			t.Fatal(err)
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
		requestEdge(t, port, config.Domains[0], http.MethodGet, "/external-purge", nil)
		if counters.count("GET /external-purge ") != 2 {
			t.Fatal("external PURGE did not evict the URL")
		}
	})

	t.Run("stale if error and response header switches", func(t *testing.T) {
		config.Version++
		cache.TTL.DefaultSeconds = 1
		cache.TTL.Status["200"] = 1
		cache.Stale.Enabled = true
		cache.Stale.IfErrorSeconds = 60
		config.Cache = toMap(t, cache)
		if err := manager.ApplySite(config); err != nil {
			t.Fatal(err)
		}
		first := requestEdge(t, port, config.Domains[0], http.MethodGet, "/stale", nil)
		if got := first.header.Get("Cache-Control"); got != "public, max-age=1, stale-if-error=60" {
			t.Fatalf("stale cache control=%q", got)
		}
		requestEdge(t, port, config.Domains[0], http.MethodGet, "/stale", nil)
		time.Sleep(1100 * time.Millisecond)
		staleOriginFailure.Store(true)
		stale := requestEdge(t, port, config.Domains[0], http.MethodGet, "/stale", nil)
		staleOriginFailure.Store(false)
		if stale.status != http.StatusOK || stale.body != first.body || stale.header.Get("X-Cache") != "STALE" {
			t.Fatalf("stale response status=%d body=%q x-cache=%q", stale.status, stale.body, stale.header.Get("X-Cache"))
		}
		if counters.count("GET /stale ") != 2 {
			t.Fatalf("stale revalidation origin count=%d", counters.count("GET /stale "))
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

func primeCache(t *testing.T, counters *originCounters, port int, host, path string) {
	t.Helper()
	requestEdge(t, port, host, http.MethodGet, path, nil)
	requestEdge(t, port, host, http.MethodGet, path, nil)
	if count := counters.count("GET " + path + " "); count != 1 {
		t.Fatalf("failed to prime %s, origin count=%d", path, count)
	}
}
