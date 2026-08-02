package cachematch

import (
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
