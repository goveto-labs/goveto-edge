package cacheheaders

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

func TestCacheResult(t *testing.T) {
	for input, want := range map[string]string{
		"Goveto; hit; ttl=10":          "HIT",
		"Goveto; hit; ttl=0":           "STALE",
		"Goveto; hit; ttl=-1":          "STALE",
		"Goveto; fwd=uri-miss; stored": "MISS",
		"Goveto; fwd=stale":            "STALE",
		"":                             "BYPASS",
	} {
		if got := cacheResult(input); got != want {
			t.Fatalf("cacheResult(%q)=%q want %q", input, got, want)
		}
	}
}

type cookieHandler struct{}

func (cookieHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) error {
	w.Header().Set("Set-Cookie", "session=secret")
	w.WriteHeader(http.StatusOK)
	return nil
}

func TestHandlerProtectsSetCookieResponses(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	handler := Handler{DefaultTTL: 300}
	if err := handler.ServeHTTP(recorder, request, caddyhttp.Handler(cookieHandler{})); err != nil {
		t.Fatal(err)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("Cache-Control=%q", got)
	}
}

func TestHandlerAddsStaleIfErrorToGeneratedCacheControl(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	handler := Handler{DefaultTTL: 300, StaleIfErrorTTL: 60, StaleWhileTTL: 30}
	if err := handler.ServeHTTP(recorder, request, caddyhttp.Handler(emptyHandler{})); err != nil {
		t.Fatal(err)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "public, s-maxage=300, max-age=300, stale-while-revalidate=30, stale-if-error=60" {
		t.Fatalf("Cache-Control=%q", got)
	}
}

func TestHandlerOverridesEdgeAndClientTTL(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) error {
		w.Header().Set("Cache-Control", "public, max-age=20, s-maxage=30")
		w.WriteHeader(http.StatusOK)
		return nil
	})
	handler := Handler{DefaultTTL: 300, OverrideClientTTL: true, ClientTTL: 60}
	if err := handler.ServeHTTP(recorder, request, next); err != nil {
		t.Fatal(err)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "public, max-age=60, s-maxage=300" {
		t.Fatalf("Cache-Control=%q", got)
	}
}

func TestHandlerBypassesConfiguredResponseCacheControl(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) error {
		w.Header().Set("Cache-Control", "public, max-age=0")
		w.WriteHeader(http.StatusOK)
		return nil
	})
	handler := Handler{DefaultTTL: 300, BypassCacheControl: []string{"max-age=0"}}
	if err := handler.ServeHTTP(recorder, request, next); err != nil {
		t.Fatal(err)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control=%q", got)
	}
}

func TestHandlerPreservesExplicitCacheDirectives(t *testing.T) {
	for _, directive := range []string{"private", "no-store", "no-cache", "must-revalidate"} {
		t.Run(directive, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
			handler := Handler{DefaultTTL: 300, StaleIfErrorTTL: 60, StaleWhileTTL: 30}
			next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) error {
				w.Header().Set("Cache-Control", directive)
				w.WriteHeader(http.StatusOK)
				return nil
			})
			if err := handler.ServeHTTP(recorder, request, next); err != nil {
				t.Fatal(err)
			}
			if got := recorder.Header().Get("Cache-Control"); got != directive {
				t.Fatalf("Cache-Control=%q, want %q", got, directive)
			}
		})
	}
}

func TestHandlerDoesNotAddSWRToMandatoryRevalidation(t *testing.T) {
	for _, directive := range []string{"no-cache", "must-revalidate", "proxy-revalidate"} {
		t.Run(directive, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			calls := 0
			next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) error {
				calls++
				w.Header().Set("Cache-Control", "public, max-age=1, "+directive)
				w.Header().Set("Cache-Status", "Goveto; hit")
				w.Header().Set("Age", "2")
				w.WriteHeader(http.StatusOK)
				return nil
			})
			handler := Handler{XCache: true, Age: true, StaleWhileTTL: 30, BackgroundRevalidate: true}
			if err := handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://example.test/", nil), next); err != nil {
				t.Fatal(err)
			}
			if hasCacheDirective(recorder.Header().Get("Cache-Control"), "stale-while-revalidate") {
				t.Fatalf("SWR was added to %s response: %q", directive, recorder.Header().Get("Cache-Control"))
			}
			if calls != 1 {
				t.Fatalf("mandatory revalidation directive %s triggered %d handler calls", directive, calls)
			}
		})
	}
}

func TestHandlerOverridesPublicResponseWithSetCookie(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) error {
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.Header().Set("Set-Cookie", "session=secret")
		w.WriteHeader(http.StatusOK)
		return nil
	})
	if err := (Handler{DefaultTTL: 300}).ServeHTTP(recorder, request, next); err != nil {
		t.Fatal(err)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("Cache-Control=%q", got)
	}
}

func TestHandlerRequestsStaleForSWRUnlessRevalidationRequired(t *testing.T) {
	for _, test := range []struct {
		name, initial, want string
	}{
		{name: "empty", want: "max-stale=30"},
		{name: "preserve", initial: "min-fresh=5", want: "min-fresh=5, max-stale=30"},
		{name: "no cache", initial: "no-cache", want: "no-cache"},
		{name: "existing", initial: "max-stale=5", want: "max-stale=5"},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
			request.Header.Set("Cache-Control", test.initial)
			next := caddyhttp.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) error {
				if got := request.Header.Get("Cache-Control"); got != test.want {
					t.Fatalf("request Cache-Control=%q, want %q", got, test.want)
				}
				return nil
			})
			if err := (Handler{StaleWhileTTL: 30}).ServeHTTP(recorder, request, next); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNormalizeContentRangeCorrectsExclusiveEnd(t *testing.T) {
	header := http.Header{"Content-Range": {"bytes 100-200/1000"}, "Content-Length": {"100"}}
	normalizeContentRange(header)
	if got := header.Get("Content-Range"); got != "bytes 100-199/1000" {
		t.Fatalf("Content-Range=%q", got)
	}
	header.Set("Content-Range", "bytes 100-199/1000")
	normalizeContentRange(header)
	if got := header.Get("Content-Range"); got != "bytes 100-199/1000" {
		t.Fatalf("valid Content-Range changed to %q", got)
	}
}

func TestApplyCacheTTLStartsNewEdgeFreshnessLifetime(t *testing.T) {
	header := http.Header{
		"Age":           {"3600"},
		"Date":          {time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)},
		"Cache-Control": {"public, max-age=86400"},
	}
	applyCacheTTL(header, 300, Handler{})
	if header.Get("Age") != "" || header.Get("Date") != "" {
		t.Fatalf("upstream cache age survived edge TTL override: %v", header)
	}
	if got := header.Get("Cache-Control"); got != "public, max-age=86400, s-maxage=300" {
		t.Fatalf("Cache-Control=%q", got)
	}
}

func TestHandlerRejectsIncompleteUpstreamResponse(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) error {
		w.Header().Set("Content-Length", "10")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("xx"))
		return nil
	})
	err := (Handler{}).ServeHTTP(recorder, request, next)
	if !errors.Is(err, ErrIncompleteResponse) {
		t.Fatalf("error=%v, want ErrIncompleteResponse", err)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control=%q", got)
	}
}

func TestHandlerStartsDetachedSWRRevalidation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, request *http.Request) error {
		if hasCacheDirective(request.Header.Get("Cache-Control"), "no-cache") {
			close(started)
			<-release
			return nil
		}
		w.Header().Set("Cache-Control", "public, max-age=1, stale-while-revalidate=30")
		w.Header().Set("Cache-Status", "Goveto; hit; ttl=-1")
		w.Header().Set("Age", "2")
		w.Header().Set("Content-Length", "2")
		_, _ = w.Write([]byte("ok"))
		return nil
	})
	handler := Handler{XCache: true, Age: true, StaleWhileTTL: 30, BackgroundRevalidate: true}
	done := make(chan error, 1)
	go func() {
		done <- handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://example.test/", nil), next)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("background revalidation was not started")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("stale response waited for background revalidation")
	}
	close(release)
}

func TestHandlerTreatsLegacyPragmaNoCacheAsRevalidation(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	request.Header.Set("Pragma", "no-cache")
	next := caddyhttp.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) error {
		if got := request.Header.Get("Cache-Control"); got != "no-cache" {
			t.Fatalf("legacy revalidation request was translated to %q", got)
		}
		return nil
	})
	if err := (Handler{StaleWhileTTL: 30}).ServeHTTP(httptest.NewRecorder(), request, next); err != nil {
		t.Fatal(err)
	}
}

func TestHandlerIgnoresExternalRequestCacheControlWhenConfigured(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	request.Header.Set("Cache-Control", "no-cache")
	request.Header.Set("Pragma", "no-cache")
	next := caddyhttp.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) error {
		if got := request.Header.Get("Cache-Control"); got != "" {
			t.Fatalf("request Cache-Control=%q, want empty", got)
		}
		if got := request.Header.Get("Pragma"); got != "" {
			t.Fatalf("request Pragma=%q, want empty", got)
		}
		return nil
	})
	if err := (Handler{IgnoreRequestCacheControl: true}).ServeHTTP(httptest.NewRecorder(), request, next); err != nil {
		t.Fatal(err)
	}
	if request.Header.Get("Cache-Control") != "no-cache" || request.Header.Get("Pragma") != "no-cache" {
		t.Fatal("handler mutated the original request headers")
	}
}

func TestControlledSWRAllowsFollowersAndRunsOneRefresh(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	var refreshes atomic.Int32
	next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, request *http.Request) error {
		if hasCacheDirective(request.Header.Get("Cache-Control"), "no-cache") {
			if refreshes.Add(1) == 1 {
				close(started)
			}
			<-release
			return nil
		}
		w.Header().Set("Cache-Control", "public, max-age=1")
		w.Header().Set("Cache-Status", "Goveto; hit; ttl=-1")
		w.Header().Set("Age", "2")
		w.Header().Set("Content-Length", "2")
		_, _ = w.Write([]byte("ok"))
		return nil
	})
	handler := Handler{
		XCache: true, Age: true, StaleWhileTTL: 30, BackgroundRevalidate: true,
		Coalesce: true, SiteID: "site",
	}
	go func() {
		firstDone <- handler.ServeHTTP(
			httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://example.test/item", nil), next,
		)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first refresh did not start")
	}

	followerDone := make(chan error, 1)
	go func() {
		followerDone <- handler.ServeHTTP(
			httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://example.test/item", nil), next,
		)
	}()
	select {
	case err := <-followerDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("stale follower was blocked behind revalidation")
	}
	if got := refreshes.Load(); got != 1 {
		t.Fatalf("refreshes=%d, want 1", got)
	}
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("first stale response waited for revalidation")
	}
	close(release)
}

func TestInnerHandlerRemovesUpstreamSWRAndOuterRestoresPolicy(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	recorder := httptest.NewRecorder()
	origin := caddyhttp.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) error {
		w.Header().Set("Cache-Control", `public, extension="field,other", max-age=10, stale-while-revalidate=300`)
		w.Header().Set("X-Goveto-Origin-Content-Length", "999")
		w.Header().Set("X-Goveto-Origin-Method", http.MethodPost)
		w.Header().Set("Content-Length", "2")
		_, _ = w.Write([]byte("ok"))
		return nil
	})
	inner := Handler{ValidateUpstream: true}
	outer := Handler{StaleWhileTTL: 30, BackgroundRevalidate: true}
	if err := outer.ServeHTTP(recorder, request, caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		return inner.ServeHTTP(w, r, origin)
	})); err != nil {
		t.Fatal(err)
	}
	if got := recorder.Header().Get("Cache-Control"); got != `public, extension="field,other", max-age=10, stale-while-revalidate=30` {
		t.Fatalf("Cache-Control=%q", got)
	}
	if got := recorder.Header().Get("X-Goveto-Origin-Content-Length"); got != "" {
		t.Fatalf("internal content length leaked: %q", got)
	}
	if got := recorder.Header().Get("X-Goveto-Origin-Method"); got != "" {
		t.Fatalf("internal method leaked: %q", got)
	}
}

func TestValidatedHandlerConvertsAbortToBadGateway(t *testing.T) {
	recorder := httptest.NewRecorder()
	next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) error {
		w.Header().Set("Content-Length", "10")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("xx"))
		panic(http.ErrAbortHandler)
	})
	err := (Handler{ValidateUpstream: true}).ServeHTTP(
		recorder, httptest.NewRequest(http.MethodGet, "http://example.test/", nil), next,
	)
	if err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusBadGateway || recorder.Body.Len() != 0 || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("abort response=%d/%q headers=%v", recorder.Code, recorder.Body.String(), recorder.Header())
	}
}

func TestHandlerRecoversUnsatisfiableRangeFromCacheEngine(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://example.test/video", nil)
	request.Header.Set("Range", "bytes=1000-1100")
	next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) error {
		w.Header().Set("Content-Range", "bytes */100")
		panic("range slice outside response")
	})
	if err := (Handler{}).ServeHTTP(recorder, request, next); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusRequestedRangeNotSatisfiable || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("range response=%d headers=%v", recorder.Code, recorder.Header())
	}
}

func TestHandlerDoesNotHideUnrelatedRangePanic(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "http://example.test/video", nil)
	request.Header.Set("Range", "bytes=0-99")
	next := caddyhttp.HandlerFunc(func(http.ResponseWriter, *http.Request) error {
		panic("unrelated failure")
	})
	defer func() {
		if recovered := recover(); recovered != "unrelated failure" {
			t.Fatalf("recovered panic=%v, want unrelated failure", recovered)
		}
	}()
	_ = (Handler{}).ServeHTTP(httptest.NewRecorder(), request, next)
}

type emptyHandler struct{}

func (emptyHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) error {
	w.WriteHeader(http.StatusOK)
	return nil
}
