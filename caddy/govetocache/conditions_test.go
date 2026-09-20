package govetocache

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"goveto-edge/internal/cacherange"
)

func TestCachedClientPreconditions(t *testing.T) {
	date := "Wed, 16 Sep 2026 10:00:00 GMT"
	for _, stale := range []bool{false, true} {
		for _, test := range []struct {
			name, method string
			headers      http.Header
			status       int
			body         string
			ranged       bool
		}{
			{"etag", "GET", http.Header{"If-None-Match": {`"v1"`}}, 304, "", false},
			{"weak etag", "GET", http.Header{"If-None-Match": {`W/"v1"`}}, 304, "", false},
			{"etag list", "GET", http.Header{"If-None-Match": {`"other,tag", "v1"`}}, 304, "", false},
			{"mismatch", "GET", http.Header{"If-Match": {`"other"`}}, 412, "", false},
			{"weak if match", "GET", http.Header{"If-Match": {`W/"v1"`}}, 412, "", false},
			{"wildcard", "HEAD", http.Header{"If-None-Match": {"*"}}, 304, "", false},
			{"modified", "GET", http.Header{"If-Modified-Since": {date}}, 304, "", false},
			{"unmodified", "GET", http.Header{"If-Unmodified-Since": {"Tue, 15 Sep 2026 10:00:00 GMT"}}, 412, "", false},
			{"etag precedence", "GET", http.Header{"If-None-Match": {`"other"`}, "If-Modified-Since": {date}}, 200, "body", false},
			{"match precedence", "GET", http.Header{"If-Match": {`"v1"`}, "If-Unmodified-Since": {"Tue, 15 Sep 2026 10:00:00 GMT"}}, 200, "body", false},
			{"head", "HEAD", nil, 200, "", false},
			{"range precondition", "GET", http.Header{"If-None-Match": {`"v1"`}}, 304, "", true},
			{"if range", "GET", http.Header{"If-Range": {`"v1"`}}, 206, "bo", true},
			{"if range mismatch", "GET", http.Header{"If-Range": {`"other"`}}, 200, "body", true},
			{"weak if range", "GET", http.Header{"If-Range": {`W/"v1"`}}, 200, "body", true},
		} {
			name := "fresh/" + test.name
			if stale {
				name = "revalidated/" + test.name
			}
			t.Run(name, func(t *testing.T) {
				storage, dir := newTestCache(t, time.Minute)
				h := &Handler{SiteID: "conditional", storage: storage, Path: dir, DefaultTTL: 60, XCache: true}
				calls := 0
				next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
					calls++
					w.Header().Set("ETag", `"v1"`)
					w.Header().Set("Last-Modified", date)
					if r.Header.Get("If-None-Match") == `"v1"` {
						w.WriteHeader(304)
						return nil
					}
					w.Header().Set("Content-Length", "4")
					_, err := io.WriteString(w, "body")
					return err
				})
				prime := httptest.NewRequest("GET", "http://example.test/asset", nil)
				if err := h.ServeHTTP(httptest.NewRecorder(), prime, next); err != nil {
					t.Fatal(err)
				}
				if stale {
					expireTestEntry(t, h, prime)
				}
				request := httptest.NewRequest(test.method, prime.URL.String(), nil)
				for name, values := range test.headers {
					request.Header[name] = values
				}
				if test.ranged {
					request.Header.Set("Range", "bytes=0-1")
					request = request.WithContext(cacherange.WithContext(request.Context(), cacherange.Spec{Start: 0, End: 1}))
				}
				response := httptest.NewRecorder()
				if err := h.ServeHTTP(response, request, next); err != nil {
					t.Fatal(err)
				}
				if response.Code != test.status || response.Body.String() != test.body {
					t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
				}
				wantCalls := 1
				if stale {
					wantCalls++
				}
				if calls != wantCalls {
					t.Fatalf("origin calls=%d want=%d", calls, wantCalls)
				}
			})
		}
	}
}

func TestChangedOriginResponseEvaluatesClientPreconditions(t *testing.T) {
	for _, test := range []struct {
		name             string
		stale, oversized bool
		header, value    string
		status           int
	}{
		{"new representation", true, false, "If-None-Match", `"v2"`, 304},
		{"new representation mismatch", true, false, "If-Match", `"v1"`, 412},
		{"overflow not modified", true, true, "If-None-Match", `"v2"`, 304},
		{"overflow mismatch", true, true, "If-Match", `"v1"`, 412},
		{"streaming miss", false, false, "If-None-Match", `"v2"`, 304},
		{"streaming bypass", false, true, "If-Match", `"v1"`, 412},
	} {
		t.Run(test.name, func(t *testing.T) {
			storage, dir := newTestCache(t, time.Minute)
			h := &Handler{SiteID: "changed", storage: storage, Path: dir, DefaultTTL: 60, MaxBodyBytes: 8}
			request := httptest.NewRequest("GET", "http://example.test/asset", nil)
			if test.stale {
				prime := caddyhttp.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) error {
					w.Header().Set("ETag", `"v1"`)
					_, err := io.WriteString(w, "old")
					return err
				})
				if err := h.ServeHTTP(httptest.NewRecorder(), request, prime); err != nil {
					t.Fatal(err)
				}
				expireTestEntry(t, h, request)
			}
			request.Header.Set(test.header, test.value)
			origin := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
				if test.stale && (r.Header.Get("If-None-Match") != `"v1"` || r.Header.Get("If-Match") != "") {
					t.Fatalf("client preconditions leaked into revalidation: %v", r.Header)
				}
				w.Header().Set("ETag", `"v2"`)
				body := "new"
				if test.oversized {
					body = strings.Repeat("x", 32)
				}
				_, err := io.WriteString(w, body)
				return err
			})
			response := httptest.NewRecorder()
			if err := h.ServeHTTP(response, request, origin); err != nil {
				t.Fatal(err)
			}
			if response.Code != test.status || response.Body.Len() != 0 || response.Header().Get("ETag") != `"v2"` {
				t.Fatalf("status=%d body=%q headers=%v", response.Code, response.Body.String(), response.Header())
			}
		})
	}
}
