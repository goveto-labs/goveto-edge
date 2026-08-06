package govetocache

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

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
	handler := Handler{HashKey: true, HideKey: true, XCache: true}
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
