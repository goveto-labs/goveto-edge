package waf

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"

	"goveto-edge/internal/policy"
)

type nextHandler struct{ calls int }

func (h *nextHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) error {
	h.calls++
	w.WriteHeader(http.StatusOK)
	return nil
}

func TestHandlerBlocksPresetsAndComplexRules(t *testing.T) {
	handler := Handler{SiteID: "site", WAF: policy.DefaultWAFPolicy()}
	handler.WAF.Enabled = true
	handler.WAF.Groups = []policy.WAFRuleGroup{{
		ID:       "admin-non-office",
		Enabled:  true,
		Operator: "AND",
		Action:   "BLOCK",
		Rules: []policy.WAFRequestRule{
			{Field: "PATH", Operator: "PREFIX", Value: "/admin"},
			{Field: "CLIENT_IP", Operator: "CIDR", Values: []string{"192.0.2.0/24"}, Negate: true},
		},
	}}
	if err := handler.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		url        string
		remoteAddr string
		userAgent  string
		wantStatus int
		wantRule   string
	}{
		{url: "http://example.test/?id=1%20UNION%20SELECT%20password%20FROM%20users", remoteAddr: "192.0.2.10:1234", wantStatus: 403, wantRule: "preset:SQL_INJECTION"},
		{url: "http://example.test/admin", remoteAddr: "198.51.100.2:1234", wantStatus: 403, wantRule: "admin-non-office"},
		{url: "http://example.test/admin", remoteAddr: "192.0.2.10:1234", wantStatus: 200},
		{url: "http://example.test/", remoteAddr: "192.0.2.10:1234", userAgent: "sqlmap/1.8", wantStatus: 403, wantRule: "preset:BAD_BOTS"},
	} {
		next := &nextHandler{}
		request := httptest.NewRequest(http.MethodGet, test.url, nil)
		request.RemoteAddr = test.remoteAddr
		request.Header.Set("User-Agent", test.userAgent)
		response := httptest.NewRecorder()
		if err := handler.ServeHTTP(response, request, caddyhttp.Handler(next)); err != nil {
			t.Fatal(err)
		}
		if response.Code != test.wantStatus || response.Header().Get("X-Goveto-WAF-Rule") != test.wantRule {
			t.Fatalf("url=%s status=%d rule=%q", test.url, response.Code, response.Header().Get("X-Goveto-WAF-Rule"))
		}
	}
}

func TestHandlerMonitorAndRateLimit(t *testing.T) {
	handler := Handler{
		SiteID: "site-monitor",
		WAF: policy.WAFPolicy{
			Enabled: true,
			Mode:    "MONITOR",
			Groups: []policy.WAFRuleGroup{{
				ID: "watch-admin", Enabled: true, Operator: "AND", Action: "BLOCK",
				Rules: []policy.WAFRequestRule{{Field: "PATH", Operator: "PREFIX", Value: "/admin"}},
			}},
		},
		RateLimit: policy.RateLimitPolicy{Enabled: true, Rules: []policy.RateLimitRule{{
			ID: "cc", Enabled: true, Key: "CLIENT_IP", Requests: 2, WindowSeconds: 60,
		}}},
	}
	if err := handler.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}

	for index := 0; index < 3; index++ {
		request := httptest.NewRequest(http.MethodGet, "http://example.test/admin", nil)
		request.RemoteAddr = "198.51.100.9:4321"
		response := httptest.NewRecorder()
		next := &nextHandler{}
		if err := handler.ServeHTTP(response, request, caddyhttp.Handler(next)); err != nil {
			t.Fatal(err)
		}
		if index < 2 && (response.Code != 200 || response.Header().Get("X-Goveto-WAF") != "MONITOR") {
			t.Fatalf("request %d was not monitored: code=%d headers=%v", index, response.Code, response.Header())
		}
		if index == 2 && response.Code != http.StatusTooManyRequests {
			t.Fatalf("third request status=%d, want 429", response.Code)
		}
	}
}

func TestAllowGroupOverridesManagedPreset(t *testing.T) {
	handler := Handler{SiteID: "site-allow", WAF: policy.DefaultWAFPolicy()}
	handler.WAF.Enabled = true
	handler.WAF.Groups = []policy.WAFRuleGroup{{
		ID: "office", Enabled: true, Operator: "AND", Action: "ALLOW",
		Rules: []policy.WAFRequestRule{{Field: "CLIENT_IP", Operator: "CIDR", Values: []string{"192.0.2.0/24"}}},
	}}
	if err := handler.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://example.test/?id=1%20UNION%20SELECT%20password%20FROM%20users", nil)
	request.RemoteAddr = "192.0.2.10:1234"
	response := httptest.NewRecorder()
	next := &nextHandler{}
	if err := handler.ServeHTTP(response, request, caddyhttp.Handler(next)); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || next.calls != 1 {
		t.Fatalf("allow group did not bypass preset: status=%d calls=%d", response.Code, next.calls)
	}
}

func BenchmarkWAFRuleEvaluation(b *testing.B) {
	handler := Handler{SiteID: "bench", WAF: policy.DefaultWAFPolicy()}
	handler.WAF.Enabled = true
	handler.WAF.Groups = []policy.WAFRuleGroup{{
		ID: "api", Enabled: true, Operator: "AND", Action: "BLOCK",
		Rules: []policy.WAFRequestRule{
			{Field: "PATH", Operator: "PREFIX", Value: "/api/"},
			{Field: "HEADER", Name: "X-Token", Operator: "REGEX", Value: `^[a-z0-9]{16}$`, Negate: true},
		},
	}}
	if err := handler.Provision(caddy.Context{}); err != nil {
		b.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://example.test/assets/app.js", nil)
	request.RemoteAddr = "192.0.2.1:1234"
	data := requestData{request: request, ip: "192.0.2.1"}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		handler.matchWAF(data)
	}
}

func BenchmarkRateLimiter(b *testing.B) {
	store := &counterStore{entries: map[string]counter{}}
	rule := policy.RateLimitRule{Requests: 1_000_000, WindowSeconds: 60}
	now := time.Now()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			store.allow("192.0.2.1", now, rule)
		}
	})
}
