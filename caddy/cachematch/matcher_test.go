package cachematch

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"goveto-edge/internal/policy"
)

func TestMatcherGroups(t *testing.T) {
	m := Matcher{Conditions: policy.CacheConditions{GroupOperator: "AND", Groups: []policy.CacheConditionGroup{
		{Operator: "OR", Rules: []policy.CacheConditionRule{{Type: "EXTENSION", Values: []string{"css"}}, {Type: "PATH_PREFIX", Values: []string{"/assets/"}}}},
		{Operator: "AND", Rules: []policy.CacheConditionRule{{Type: "PATH_REGEX", Value: `^/assets/`}}},
	}}}
	m.compiled = [][]*regexp.Regexp{{nil, nil}, {regexp.MustCompile(`^/assets/`)}}
	if !m.Match(httptest.NewRequest("GET", "http://example.test/assets/app.js", nil)) {
		t.Fatal("expected grouped condition to match")
	}
	if m.Match(httptest.NewRequest("GET", "http://example.test/app.css", nil)) {
		t.Fatal("expected outer AND to reject")
	}
}

func TestMatcherRejectsRangeWhenRangeCachingIsDisabled(t *testing.T) {
	m := Matcher{
		CacheRangeRequests: false,
		Conditions: policy.CacheConditions{GroupOperator: "OR", Groups: []policy.CacheConditionGroup{
			{Operator: "OR", Rules: []policy.CacheConditionRule{{Type: "ALL"}}},
		}},
	}
	m.compiled = [][]*regexp.Regexp{{nil}}
	request := httptest.NewRequest(http.MethodGet, "http://example.test/video.mp4", nil)
	request.Header.Set("Range", "bytes=0-99")
	if m.Match(request) {
		t.Fatal("range request matched while range caching was disabled")
	}
	request.Header.Del("Range")
	if !m.Match(request) {
		t.Fatal("ordinary GET should still match")
	}
}

func TestCacheableRangeAcceptsSingleStartRangesOnly(t *testing.T) {
	for _, value := range []string{"bytes=0-99", "BYTES = 5 - 5"} {
		if !cacheableRange(value) {
			t.Fatalf("range %q should be cacheable", value)
		}
	}
	for _, value := range []string{"bytes=-100", "bytes=100-", "bytes=0-1,3-4", "items=0-1", "bytes=9-1", "bytes=x-1"} {
		if cacheableRange(value) {
			t.Fatalf("range %q should bypass cache", value)
		}
	}
}

func TestMatcherBypassesConfiguredRequestCacheControl(t *testing.T) {
	m := Matcher{
		Conditions: policy.CacheConditions{GroupOperator: "OR", Groups: []policy.CacheConditionGroup{{
			Operator: "OR", Rules: []policy.CacheConditionRule{{Type: "ALL"}},
		}}},
		BypassCacheControl: []string{"no-store", "max-age=0"},
	}
	for _, value := range []string{"no-store", "public, max-age=0"} {
		request := httptest.NewRequest(http.MethodGet, "http://example.test/asset", nil)
		request.Header.Set("Cache-Control", value)
		if m.Match(request) {
			t.Fatalf("request with Cache-Control %q should bypass", value)
		}
	}
}

func TestMatcherBypassesConditionalRange(t *testing.T) {
	m := Matcher{
		CacheRangeRequests: true,
		Conditions: policy.CacheConditions{GroupOperator: "OR", Groups: []policy.CacheConditionGroup{
			{Operator: "OR", Rules: []policy.CacheConditionRule{{Type: "ALL"}}},
		}},
		compiled: [][]*regexp.Regexp{{nil}},
	}
	request := httptest.NewRequest("GET", "http://example.test/video", nil)
	request.Header.Set("Range", "bytes=0-99")
	request.Header.Set("If-Range", `"etag"`)
	if m.Match(request) {
		t.Fatal("If-Range request must bypass the cache engine")
	}
	request.Header.Del("If-Range")
	request.Header["Range"] = []string{"bytes=0-99", "bytes=200-299"}
	if m.Match(request) {
		t.Fatal("multiple Range fields must bypass the cache engine")
	}
}

func BenchmarkCacheRuleEvaluation(b *testing.B) {
	complex := benchmarkMatchers(b, 32)
	cases := []struct {
		name     string
		matchers []Matcher
		path     string
	}{
		{name: "catch_all", matchers: benchmarkMatchers(b, 1), path: "/bytes/16384"},
		{name: "first_of_32", matchers: complex, path: "/assets/app.css"},
		{name: "late_of_32", matchers: complex, path: "/cache-rules/late/123.bin"},
		{name: "fallback_of_32", matchers: complex, path: "/bytes/16384"},
	}
	for _, benchmark := range cases {
		request := httptest.NewRequest(http.MethodGet, "https://cache.example.test"+benchmark.path, nil)
		b.Run("Serial/"+benchmark.name, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				if !matchFirst(benchmark.matchers, request) {
					b.Fatal("request did not match a cache rule")
				}
			}
		})
		b.Run("Parallel/"+benchmark.name, func(b *testing.B) {
			b.ReportAllocs()
			b.RunParallel(func(worker *testing.PB) {
				for worker.Next() {
					if !matchFirst(benchmark.matchers, request) {
						b.Error("request did not match a cache rule")
						return
					}
				}
			})
		})
	}
}

func benchmarkMatchers(b *testing.B, count int) []Matcher {
	b.Helper()
	if count == 1 {
		return []Matcher{{
			Conditions: policy.CacheConditions{GroupOperator: "OR", Groups: []policy.CacheConditionGroup{{
				Operator: "OR", Rules: []policy.CacheConditionRule{{Type: "ALL"}},
			}}},
			compiled: [][]*regexp.Regexp{{nil}},
		}}
	}
	matchers := make([]Matcher, 0, count)
	matchers = append(matchers, Matcher{
		Conditions: policy.CacheConditions{GroupOperator: "OR", Groups: []policy.CacheConditionGroup{{
			Operator: "OR", Rules: []policy.CacheConditionRule{{Type: "EXTENSION", Values: []string{"css"}}},
		}}},
		compiled: [][]*regexp.Regexp{{nil}},
	})
	for index := 1; index < count-2; index++ {
		compiled := regexp.MustCompile(fmt.Sprintf(`^/unmatched/%d/[0-9]+$`, index))
		matchers = append(matchers, Matcher{
			Conditions: policy.CacheConditions{GroupOperator: "AND", Groups: []policy.CacheConditionGroup{
				{Operator: "OR", Rules: []policy.CacheConditionRule{
					{Type: "EXTENSION", Values: []string{fmt.Sprintf("cachebench%d", index)}},
					{Type: "PATH_PREFIX", Values: []string{fmt.Sprintf("/unmatched/%d/", index)}},
				}},
				{Operator: "AND", Rules: []policy.CacheConditionRule{{Type: "PATH_REGEX", Value: compiled.String()}}},
			}},
			compiled: [][]*regexp.Regexp{{nil, nil}, {compiled}},
		})
	}
	late := regexp.MustCompile(`^/cache-rules/late/[0-9]+[.]bin$`)
	matchers = append(matchers,
		Matcher{
			Conditions: policy.CacheConditions{GroupOperator: "AND", Groups: []policy.CacheConditionGroup{
				{Operator: "OR", Rules: []policy.CacheConditionRule{
					{Type: "EXTENSION", Values: []string{"bin"}},
					{Type: "PATH_PREFIX", Values: []string{"/cache-rules/late/"}},
				}},
				{Operator: "AND", Rules: []policy.CacheConditionRule{{Type: "PATH_REGEX", Value: late.String()}}},
			}},
			compiled: [][]*regexp.Regexp{{nil, nil}, {late}},
		},
		Matcher{
			Conditions: policy.CacheConditions{GroupOperator: "OR", Groups: []policy.CacheConditionGroup{{
				Operator: "OR", Rules: []policy.CacheConditionRule{{Type: "ALL"}},
			}}},
			compiled: [][]*regexp.Regexp{{nil}},
		},
	)
	if len(matchers) != count {
		b.Fatalf("created %d matchers, want %d", len(matchers), count)
	}
	return matchers
}

func matchFirst(matchers []Matcher, request *http.Request) bool {
	for index := range matchers {
		if matchers[index].Match(request) {
			return true
		}
	}
	return false
}
