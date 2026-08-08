package waf

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"

	"goveto-edge/internal/policy"
	"goveto-edge/internal/securitystate"
)

type fakeDistributedStore struct {
	mu         sync.Mutex
	counts     map[string]int
	challenges map[string]bool
	blocked    bool
	err        error
}

func (s *fakeDistributedStore) PutChallenge(_ context.Context, token string, _ time.Duration) error {
	if s.err != nil {
		return s.err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.challenges == nil {
		s.challenges = map[string]bool{}
	}
	s.challenges[token] = true
	return nil
}

func (s *fakeDistributedStore) ConsumeChallenge(_ context.Context, token string) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.challenges[token] {
		return false, nil
	}
	delete(s.challenges, token)
	return true, nil
}

func (s *fakeDistributedStore) Allow(_ context.Context, siteID, ruleID, value string, rule policy.RateLimitRule) (bool, time.Duration, error) {
	if s.err != nil {
		return false, 0, s.err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.counts == nil {
		s.counts = map[string]int{}
	}
	key := siteID + ":" + ruleID + ":" + value
	s.counts[key]++
	return s.counts[key] <= rule.Requests+rule.Burst, time.Minute, nil
}

func (s *fakeDistributedStore) Blocked(context.Context, string, string) (bool, time.Duration, error) {
	return s.blocked, time.Minute, s.err
}

type nextHandler struct {
	calls int
	tags  string
}

func (h *nextHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) error {
	h.calls++
	h.tags = strings.Join(r.Header.Values("X-Goveto-WAF-Tags"), ",")
	w.WriteHeader(http.StatusOK)
	return nil
}

func TestHandlerResponseRedirectAndTagActions(t *testing.T) {
	groups := []policy.WAFRuleGroup{
		{ID: "page", Enabled: true, Operator: "AND", Action: policy.WAFActionShowPage, StatusCode: 451, Response: policy.WAFResponse{Type: policy.WAFResponseHTML, Body: "<h1>Unavailable here</h1>"}, Rules: []policy.WAFRequestRule{{Field: "PATH", Operator: "EQUALS", Value: "/page"}}},
		{ID: "redirect", Enabled: true, Operator: "AND", Action: policy.WAFActionRedirect, RedirectURL: "/safe", RedirectStatus: 307, Rules: []policy.WAFRequestRule{{Field: "PATH", Operator: "EQUALS", Value: "/old"}}},
		{ID: "tag", Enabled: true, Operator: "AND", Action: policy.WAFActionTag, Tag: "trusted.bot", Rules: []policy.WAFRequestRule{{Field: "PATH", Operator: "EQUALS", Value: "/tag"}}},
	}
	handler := Handler{SiteID: "actions", WAF: policy.DefaultWAFPolicy()}
	handler.WAF.Enabled = true
	handler.WAF.Presets = nil
	handler.WAF.Groups = groups
	if err := handler.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}

	page := httptest.NewRecorder()
	if err := handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "http://example.test/page", nil), caddyhttp.Handler(&nextHandler{})); err != nil {
		t.Fatal(err)
	}
	if page.Code != 451 || page.Header().Get("Content-Type") != "text/html; charset=utf-8" || page.Body.String() != "<h1>Unavailable here</h1>" {
		t.Fatalf("custom page response=%d %q %q", page.Code, page.Header().Get("Content-Type"), page.Body.String())
	}

	redirect := httptest.NewRecorder()
	if err := handler.ServeHTTP(redirect, httptest.NewRequest(http.MethodGet, "http://example.test/old", nil), caddyhttp.Handler(&nextHandler{})); err != nil {
		t.Fatal(err)
	}
	if redirect.Code != http.StatusTemporaryRedirect || redirect.Header().Get("Location") != "/safe" {
		t.Fatalf("redirect=%d location=%q", redirect.Code, redirect.Header().Get("Location"))
	}

	next := &nextHandler{}
	tagged := httptest.NewRecorder()
	if err := handler.ServeHTTP(tagged, httptest.NewRequest(http.MethodGet, "http://example.test/tag", nil), caddyhttp.Handler(next)); err != nil {
		t.Fatal(err)
	}
	if tagged.Code != http.StatusOK || next.tags != "trusted.bot" || tagged.Header().Get("X-Goveto-WAF-Tag") != "trusted.bot" {
		t.Fatalf("tag action status=%d upstream=%q response=%q", tagged.Code, next.tags, tagged.Header().Get("X-Goveto-WAF-Tag"))
	}
}

func TestManagedPresetUsesEmbeddedDefaultBlockPage(t *testing.T) {
	handler := Handler{SiteID: "default-page", WAF: policy.DefaultWAFPolicy()}
	handler.WAF.Enabled = true
	if err := handler.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://example.test/?id=UNION%20SELECT%20x%20FROM%20y", nil)
	if err := handler.ServeHTTP(response, request, caddyhttp.Handler(&nextHandler{})); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "Request blocked") || !strings.Contains(response.Body.String(), "preset:SQL_INJECTION") {
		t.Fatalf("default page status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestProofOfWorkCaptchaGrantsClearance(t *testing.T) {
	handler := Handler{SiteID: "captcha-site", ChallengeSecret: testChallengeSecret(), WAF: policy.DefaultWAFPolicy()}
	handler.WAF.Enabled = true
	handler.WAF.Presets = nil
	handler.WAF.Groups = []policy.WAFRuleGroup{{
		ID: "shield", Enabled: true, Operator: "AND", Action: policy.WAFActionCaptcha,
		AutoBan: policy.WAFAutoBan{Enabled: true, Hits: 2, WindowSeconds: 60, BanSeconds: 300, Scope: "SITE"},
		Rules:   []policy.WAFRequestRule{{Field: "PATH", Operator: "EQUALS", Value: "/protected"}},
	}}
	if err := handler.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}
	ip := "198.51.100.20"
	baseRequest := captchaRequest("http://example.test/protected", ip)
	token, err := handler.challengeToken("shield", baseRequest, ip)
	if err != nil {
		t.Fatal(err)
	}
	proof := solveChallenge(t, token, testBrowserEnvironment(baseRequest))

	verify := captchaRequest("http://example.test/protected?__goveto_challenge="+url.QueryEscape(token)+"&__goveto_proof="+proof, ip)
	verified := httptest.NewRecorder()
	if err = handler.ServeHTTP(verified, verify, caddyhttp.Handler(&nextHandler{})); err != nil {
		t.Fatal(err)
	}
	result := verified.Result()
	if result.StatusCode != http.StatusSeeOther || len(result.Cookies()) != 1 {
		t.Fatalf("challenge completion status=%d cookies=%v", result.StatusCode, result.Cookies())
	}

	for attempt := range 2 {
		request := captchaRequest("http://example.test/protected", ip)
		request.AddCookie(result.Cookies()[0])
		response := httptest.NewRecorder()
		next := &nextHandler{}
		if err = handler.ServeHTTP(response, request, caddyhttp.Handler(next)); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusOK || next.calls != 1 || response.Header().Get("X-Goveto-WAF") != "CAPTCHA-PASS" {
			t.Fatalf("clearance attempt=%d status=%d calls=%d header=%q", attempt+1, response.Code, next.calls, response.Header().Get("X-Goveto-WAF"))
		}
	}
}

func TestDistributedChallengeStateRejectsReplay(t *testing.T) {
	handler := Handler{SiteID: "captcha-replay", ChallengeSecret: testChallengeSecret(), WAF: policy.DefaultWAFPolicy()}
	handler.WAF.Enabled = true
	handler.WAF.Presets = nil
	handler.WAF.Groups = []policy.WAFRuleGroup{{
		ID: "shield", Enabled: true, Operator: "AND", Action: policy.WAFActionCaptcha,
		Rules: []policy.WAFRequestRule{{Field: "PATH", Operator: "EQUALS", Value: "/protected"}},
	}}
	if err := handler.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}
	handler.distributed = &fakeDistributedStore{}
	ip := "198.51.100.21"
	base := captchaRequest("http://example.test/protected", ip)
	token, err := handler.challengeToken("shield", base, ip)
	if err != nil {
		t.Fatal(err)
	}
	claim, ok := handler.verifyClaim(token)
	if !ok || !claim.Stateful {
		t.Fatal("challenge was not backed by distributed state")
	}
	proof := solveChallenge(t, token, testBrowserEnvironment(base))
	target := "http://example.test/protected?__goveto_challenge=" + url.QueryEscape(token) + "&__goveto_proof=" + proof

	first := httptest.NewRecorder()
	if err = handler.ServeHTTP(first, captchaRequest(target, ip), caddyhttp.Handler(&nextHandler{})); err != nil {
		t.Fatal(err)
	}
	if first.Code != http.StatusSeeOther {
		t.Fatalf("first completion status=%d", first.Code)
	}

	replay := httptest.NewRecorder()
	if err = handler.ServeHTTP(replay, captchaRequest(target, ip), caddyhttp.Handler(&nextHandler{})); err != nil {
		t.Fatal(err)
	}
	if replay.Code != http.StatusServiceUnavailable || replay.Header().Get("X-Goveto-WAF-Challenge") != "replayed" {
		t.Fatalf("replay status=%d headers=%v", replay.Code, replay.Header())
	}
}

func TestCaptchaPageEmbedsVersionedWorkerSolver(t *testing.T) {
	handler := Handler{SiteID: "captcha-page", ChallengeSecret: testChallengeSecret(), WAF: policy.DefaultWAFPolicy()}
	handler.WAF.Enabled = true
	handler.WAF.Presets = nil
	handler.WAF.Groups = []policy.WAFRuleGroup{{
		ID: "shield", Enabled: true, Operator: "AND", Action: policy.WAFActionCaptcha,
		Rules: []policy.WAFRequestRule{{Field: "PATH", Operator: "EQUALS", Value: "/protected"}},
	}}
	if err := handler.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://example.test/protected", nil)
	request.RemoteAddr = "192.0.2.1:1234"
	response := httptest.NewRecorder()
	if err := handler.ServeHTTP(response, request, caddyhttp.Handler(&nextHandler{})); err != nil {
		t.Fatal(err)
	}
	body := response.Body.String()
	if response.Code != http.StatusServiceUnavailable || strings.Contains(body, "ZgotmplZ") ||
		!strings.Contains(body, "new Worker") || !strings.Contains(body, "Scrypt implementation bundled") {
		t.Fatalf("CAPTCHA page did not embed the worker solver correctly: status=%d body=%q", response.Code, body)
	}
	if csp := response.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "img-src data:") {
		t.Fatalf("CAPTCHA page CSP does not allow embedded state images: %q", csp)
	}
}

func TestProofOfWorkRejectsTamperingWrongBindingAndOutOfRangeProof(t *testing.T) {
	handler := Handler{SiteID: "captcha-security", ChallengeSecret: testChallengeSecret(), WAF: policy.DefaultWAFPolicy()}
	handler.WAF.Enabled = true
	handler.WAF.Presets = nil
	handler.WAF.Groups = []policy.WAFRuleGroup{{
		ID: "shield", Enabled: true, Operator: "AND", Action: policy.WAFActionCaptcha,
		Rules: []policy.WAFRequestRule{{Field: "PATH", Operator: "EQUALS", Value: "/protected"}},
	}}
	if err := handler.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}
	baseRequest := captchaRequest("http://example.test/protected", "198.51.100.20")
	token, err := handler.challengeToken("shield", baseRequest, "198.51.100.20")
	if err != nil {
		t.Fatal(err)
	}
	proof := solveChallenge(t, token, testBrowserEnvironment(baseRequest))
	outOfRange, err := decodeChallengeSolution(proof)
	if err != nil {
		t.Fatal(err)
	}
	outOfRange.Counter = powCounterMaximum + 1
	outOfRangeProof, err := encodeChallengeSolution(outOfRange)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name  string
		token string
		proof string
		ip    string
	}{
		{name: "tampered token", token: tamperToken(token), proof: proof, ip: "198.51.100.20"},
		{name: "non-canonical token", token: nonCanonicalToken(token), proof: proof, ip: "198.51.100.20"},
		{name: "wrong IP", token: token, proof: proof, ip: "198.51.100.21"},
		{name: "out of range", token: token, proof: outOfRangeProof, ip: "198.51.100.20"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := captchaRequest("http://example.test/protected?__goveto_challenge="+url.QueryEscape(test.token)+"&__goveto_proof="+test.proof, test.ip)
			response := httptest.NewRecorder()
			if err := handler.ServeHTTP(response, request, caddyhttp.Handler(&nextHandler{})); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusServiceUnavailable || len(response.Result().Cookies()) != 0 {
				t.Fatalf("invalid proof accepted: status=%d", response.Code)
			}
		})
	}
}

func TestCaptchaClearanceWorksAcrossHandlersWithSharedSecret(t *testing.T) {
	newHandler := func() Handler {
		handler := Handler{SiteID: "shared-site", ChallengeSecret: testChallengeSecret(), WAF: policy.DefaultWAFPolicy()}
		handler.WAF.Enabled = true
		handler.WAF.Presets = nil
		handler.WAF.Groups = []policy.WAFRuleGroup{{
			ID: "shield", Enabled: true, Operator: "AND", Action: policy.WAFActionCaptcha,
			Rules: []policy.WAFRequestRule{{Field: "PATH", Operator: "EQUALS", Value: "/protected"}},
		}}
		if err := handler.Provision(caddy.Context{}); err != nil {
			t.Fatal(err)
		}
		return handler
	}
	issuer, verifier := newHandler(), newHandler()
	request := captchaRequest("http://example.test/protected", "192.0.2.10")
	token, err := issuer.challengeToken("shield", request, "192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}
	claim, ok := verifier.verifyClaim(token)
	assessment, valid := verifier.validChallengeClaim(claim, request, "shield", "192.0.2.10", solveChallenge(t, token, testBrowserEnvironment(request)))
	if !ok || !valid || !assessment.Accepted {
		t.Fatal("shared challenge secret did not work across handler instances")
	}
}

func TestCaptchaRequiresPublishedChallengeSecret(t *testing.T) {
	handler := Handler{SiteID: "missing-secret", WAF: policy.DefaultWAFPolicy()}
	handler.WAF.Enabled = true
	handler.WAF.Groups = []policy.WAFRuleGroup{{
		ID: "shield", Enabled: true, Operator: "AND", Action: policy.WAFActionCaptcha,
		Rules: []policy.WAFRequestRule{{Field: "PATH", Operator: "EQUALS", Value: "/"}},
	}}
	if err := handler.Provision(caddy.Context{}); err == nil {
		t.Fatal("CAPTCHA provision should fail without a shared challenge secret")
	}
}

func testChallengeSecret() string {
	return base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
}

func tamperToken(token string) string {
	parts := strings.Split(token, ".")
	if parts[1][0] == 'A' {
		parts[1] = "B" + parts[1][1:]
	} else {
		parts[1] = "A" + parts[1][1:]
	}
	return strings.Join(parts, ".")
}

func nonCanonicalToken(token string) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	parts := strings.Split(token, ".")
	last := len(parts[1]) - 1
	index := strings.IndexByte(alphabet, parts[1][last])
	variant := (index &^ 3) | ((index + 1) & 3)
	parts[1] = parts[1][:last] + string(alphabet[variant])
	return strings.Join(parts, ".")
}

func solveChallenge(t testing.TB, token string, environment browserEnvironment) string {
	t.Helper()
	claim, err := decodeChallengeForSolver(token)
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := base64.RawURLEncoding.DecodeString(claim.Nonce)
	if err != nil {
		t.Fatal(err)
	}
	target, err := base64.RawURLEncoding.DecodeString(claim.Target)
	if err != nil {
		t.Fatal(err)
	}
	salt, err := base64.RawURLEncoding.DecodeString(claim.Salt)
	if err != nil {
		t.Fatal(err)
	}
	for counter := uint32(0); counter <= claim.MaxCounter; counter++ {
		key, deriveErr := deriveScryptKey(nonce, salt, counter, claim.ScryptN, claim.ScryptR, claim.ScryptP)
		if deriveErr != nil {
			t.Fatal(deriveErr)
		}
		if string(key[:powTargetLength]) == string(target) {
			encoded, encodeErr := encodeChallengeSolution(challengeSolution{
				Counter: counter, Key: base64.RawURLEncoding.EncodeToString(key), Environment: environment,
			})
			if encodeErr != nil {
				t.Fatal(encodeErr)
			}
			return encoded
		}
	}
	t.Fatal("deterministic challenge had no solution")
	return ""
}

func captchaRequest(target, ip string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.RemoteAddr = ip + ":1234"
	request.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/136.0 Safari/537.36")
	request.Header.Set("Accept-Language", "en-US,en;q=0.9")
	return request
}

func testBrowserEnvironment(request *http.Request) browserEnvironment {
	return browserEnvironment{
		Version: browserEnvironmentVersion, UserAgent: request.UserAgent(), Platform: "MacIntel",
		Vendor: "Google Inc.", Languages: []string{"en-US", "en"}, Timezone: "UTC",
		Cookies: true, Worker: true, WebAssembly: true, Crypto: true, Canvas: true, WebGL: true,
		WebGLVendor: "Google Inc.", WebGLRenderer: "Apple GPU", LocalStorage: true,
		SessionStorage: true, IndexedDB: true, HardwareConcurrency: 8, DeviceMemory: 8,
		ScreenWidth: 1440, ScreenHeight: 900, ColorDepth: 24, OuterWidth: 1440, OuterHeight: 900,
		AutomationSignals: []string{}, NativeTampering: []string{},
	}
}

func BenchmarkVerifyProofOfWork(b *testing.B) {
	handler := Handler{SiteID: "bench-pow", challengeKey: []byte("0123456789abcdef0123456789abcdef")}
	request := captchaRequest("http://example.test/protected", "192.0.2.1")
	token, err := handler.challengeToken("shield", request, "192.0.2.1")
	if err != nil {
		b.Fatal(err)
	}
	claim, ok := handler.verifyClaim(token)
	if !ok {
		b.Fatal("invalid generated claim")
	}
	proof := solveChallenge(b, token, testBrowserEnvironment(request))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		assessment, valid := handler.validChallengeClaim(claim, request, "shield", "192.0.2.1", proof)
		if !valid || !assessment.Accepted {
			b.Fatal("proof rejected")
		}
	}
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

func TestRateLimitPathCounterAggregatesClients(t *testing.T) {
	handler := Handler{
		SiteID: "path-rate", WAF: policy.DefaultWAFPolicy(), Access: policy.DefaultAccessPolicy(),
		RateLimit: policy.RateLimitPolicy{Enabled: true, Rules: []policy.RateLimitRule{{
			ID: "downloads", Enabled: true, Key: "PATH", Requests: 1, WindowSeconds: 60,
		}}},
	}
	if err := handler.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}
	for index, address := range []string{"192.0.2.1:1000", "198.51.100.2:2000"} {
		request := httptest.NewRequest(http.MethodGet, "http://example.test/download", nil)
		request.RemoteAddr = address
		response := httptest.NewRecorder()
		if err := handler.ServeHTTP(response, request, caddyhttp.Handler(&nextHandler{})); err != nil {
			t.Fatal(err)
		}
		want := http.StatusOK
		if index == 1 {
			want = http.StatusTooManyRequests
		}
		if response.Code != want {
			t.Fatalf("request %d status=%d want=%d", index, response.Code, want)
		}
	}
}

func TestAccessPolicyUsesOnlyTrustedProxyChain(t *testing.T) {
	access := policy.DefaultAccessPolicy()
	access.Enabled = true
	access.TrustedProxies = []string{"10.0.0.0/8"}
	access.IPBlocklist = []string{"198.51.100.0/24"}
	access.AllowedMethods = []string{"GET"}
	handler := Handler{SiteID: "access", WAF: policy.DefaultWAFPolicy(), Access: access, RateLimit: policy.DefaultRateLimitPolicy()}
	if err := handler.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	request.RemoteAddr = "10.0.0.2:1234"
	request.Header.Set("X-Forwarded-For", "203.0.113.9, 198.51.100.8")
	response := httptest.NewRecorder()
	if err := handler.ServeHTTP(response, request, caddyhttp.Handler(&nextHandler{})); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusForbidden || response.Header().Get("X-Goveto-WAF-Rule") != "access:ip-blocklist" || response.Header().Get("X-Request-ID") == "" {
		t.Fatalf("trusted proxy access decision status=%d headers=%v", response.Code, response.Header())
	}

	request = httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	request.RemoteAddr = "192.0.2.2:1234"
	request.Header.Set("X-Forwarded-For", "198.51.100.8")
	response = httptest.NewRecorder()
	if err := handler.ServeHTTP(response, request, caddyhttp.Handler(&nextHandler{})); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK {
		t.Fatalf("untrusted direct client spoofed XFF: status=%d", response.Code)
	}
}

func TestDistributedRateLimitIsSharedAcrossHandlers(t *testing.T) {
	shared := &fakeDistributedStore{}
	newHandler := func() Handler {
		handler := Handler{
			SiteID: "distributed", WAF: policy.DefaultWAFPolicy(), Access: policy.DefaultAccessPolicy(),
			RateLimit: policy.RateLimitPolicy{Enabled: true, Backend: "REDIS", FailureMode: "CLOSED", Rules: []policy.RateLimitRule{{
				ID: "api", Enabled: true, Key: "CLIENT_IP_PATH", Requests: 1, WindowSeconds: 60,
			}}},
		}
		if err := handler.Provision(caddy.Context{}); err != nil {
			t.Fatal(err)
		}
		handler.distributed, handler.distributedErr = shared, nil
		return handler
	}
	first, second := newHandler(), newHandler()
	for index, handler := range []Handler{first, second} {
		request := httptest.NewRequest(http.MethodGet, "http://example.test/api", nil)
		request.RemoteAddr = "192.0.2.10:1234"
		response := httptest.NewRecorder()
		if err := handler.ServeHTTP(response, request, caddyhttp.Handler(&nextHandler{})); err != nil {
			t.Fatal(err)
		}
		want := http.StatusOK
		if index == 1 {
			want = http.StatusTooManyRequests
		}
		if response.Code != want {
			t.Fatalf("node %d status=%d want=%d", index, response.Code, want)
		}
	}
}

func TestRateLimitRedisFailureModes(t *testing.T) {
	t.Setenv("EDGE_AGENT_REDIS_URL", "")
	for _, test := range []struct {
		mode string
		want int
	}{{mode: "OPEN", want: http.StatusOK}, {mode: "CLOSED", want: http.StatusServiceUnavailable}, {mode: "LOCAL", want: http.StatusOK}} {
		t.Run(test.mode, func(t *testing.T) {
			handler := Handler{SiteID: "failure-" + test.mode, WAF: policy.DefaultWAFPolicy(), Access: policy.DefaultAccessPolicy(), RateLimit: policy.RateLimitPolicy{
				Enabled: true, Backend: "REDIS", FailureMode: test.mode,
				Rules: []policy.RateLimitRule{{ID: "rule", Enabled: true, Key: "GLOBAL", Requests: 1, WindowSeconds: 60}},
			}}
			if err := handler.Provision(caddy.Context{}); err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			if err := handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://example.test/", nil), caddyhttp.Handler(&nextHandler{})); err != nil {
				t.Fatal(err)
			}
			if response.Code != test.want {
				t.Fatalf("status=%d want=%d", response.Code, test.want)
			}
		})
	}
}

func TestWAFExceptionBypassesSelectedRule(t *testing.T) {
	wafPolicy := policy.DefaultWAFPolicy()
	wafPolicy.Enabled = true
	wafPolicy.Exceptions = []policy.WAFException{{
		ID: "safe-path", Enabled: true, RuleIDs: []string{"preset:SQL_INJECTION"},
		Conditions: policy.RequestConditions{Groups: []policy.RequestConditionGroup{{
			Rules: []policy.WAFRequestRule{{Field: "PATH", Operator: "EQUALS", Value: "/safe"}},
		}}},
	}}
	handler := Handler{SiteID: "exceptions", WAF: wafPolicy, Access: policy.DefaultAccessPolicy(), RateLimit: policy.DefaultRateLimitPolicy()}
	if err := handler.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		path string
		want int
	}{{path: "/safe?id=UNION%20SELECT%20x%20FROM%20y", want: http.StatusOK}, {path: "/unsafe?id=UNION%20SELECT%20x%20FROM%20y", want: http.StatusForbidden}} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "http://example.test"+test.path, nil)
		if err := handler.ServeHTTP(response, request, caddyhttp.Handler(&nextHandler{})); err != nil {
			t.Fatal(err)
		}
		if response.Code != test.want {
			t.Fatalf("path=%s status=%d want=%d", test.path, response.Code, test.want)
		}
	}
}

func TestTemporaryBlockBackendDecision(t *testing.T) {
	access := policy.DefaultAccessPolicy()
	access.Enabled, access.TemporaryBlocks = true, true
	handler := Handler{SiteID: "blocks", WAF: policy.DefaultWAFPolicy(), Access: access, RateLimit: policy.DefaultRateLimitPolicy()}
	if err := handler.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}
	handler.distributed, handler.distributedErr = &fakeDistributedStore{blocked: true}, nil
	response := httptest.NewRecorder()
	if err := handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://example.test/", nil), caddyhttp.Handler(&nextHandler{})); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusForbidden || response.Header().Get("X-Goveto-WAF-Match") != "temporary_block" {
		t.Fatalf("temporary block status=%d headers=%v", response.Code, response.Header())
	}
	handler.distributed = &fakeDistributedStore{err: errors.New("redis unavailable")}
	handler.Access.TemporaryBlockFailure = "OPEN"
	response = httptest.NewRecorder()
	if err := handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://example.test/", nil), caddyhttp.Handler(&nextHandler{})); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK {
		t.Fatalf("fail-open temporary block status=%d", response.Code)
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

func TestRateLimiterFixedWindowAndBanSemantics(t *testing.T) {
	started := time.Unix(1_700_000_000, 0)
	withoutBan := &counterStore{entries: map[string]counter{}}
	rule := policy.RateLimitRule{Requests: 2, WindowSeconds: 60}
	for index := range 2 {
		if allowed, _ := withoutBan.allow("client", started.Add(time.Duration(index)*time.Second), rule); !allowed {
			t.Fatalf("request %d inside initial budget was rejected", index+1)
		}
	}
	if allowed, _ := withoutBan.allow("client", started.Add(2*time.Second), rule); allowed {
		t.Fatal("request above the fixed-window budget was allowed")
	}
	if allowed, _ := withoutBan.allow("client", started.Add(60*time.Second), rule); !allowed {
		t.Fatal("new fixed window did not restore the request budget")
	}

	withBan := &counterStore{entries: map[string]counter{}}
	rule.BanSeconds = 300
	_, _ = withBan.allow("client", started, rule)
	_, _ = withBan.allow("client", started.Add(time.Second), rule)
	if allowed, _ := withBan.allow("client", started.Add(2*time.Second), rule); allowed {
		t.Fatal("request above the budget did not trigger the ban")
	}
	if allowed, _ := withBan.allow("client", started.Add(120*time.Second), rule); allowed {
		t.Fatal("fixed-window reset bypassed an active ban")
	}
}

func TestCustomGroupMatchReportsSubRuleDetail(t *testing.T) {
	handler := Handler{SiteID: "detail", WAF: policy.DefaultWAFPolicy()}
	handler.WAF.Enabled = true
	handler.WAF.Presets = nil
	handler.WAF.Groups = []policy.WAFRuleGroup{{
		ID: "detect", Enabled: true, Operator: "OR", Action: policy.WAFActionBlock, StatusCode: http.StatusForbidden,
		Rules: []policy.WAFRequestRule{
			{Field: "PATH", Operator: "EQUALS", Value: "/safe"},
			{Field: "USER_AGENT", Operator: "CONTAINS", Value: "sqlmap"},
			{Field: "QUERY", Name: "id", Operator: "REGEX", Value: "union"},
		},
	}}
	if err := handler.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://example.test/?id=union", nil)
	request.Header.Set("User-Agent", "sqlmap/1.2")
	if err := handler.ServeHTTP(response, request, caddyhttp.Handler(&nextHandler{})); err != nil {
		t.Fatal(err)
	}
	match := response.Header().Get("X-Goveto-WAF-Match")
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d", response.Code)
	}
	for _, expected := range []string{"rule[2]:USER_AGENT:CONTAINS", "rule[3]:QUERY:REGEX"} {
		if !strings.Contains(match, expected) {
			t.Fatalf("match detail missing %q in %q", expected, match)
		}
	}
}

func TestCustomGroupMatchReportsAndGroupAllMatchedRules(t *testing.T) {
	handler := Handler{SiteID: "detail-and", WAF: policy.DefaultWAFPolicy()}
	handler.WAF.Enabled = true
	handler.WAF.Presets = nil
	handler.WAF.Groups = []policy.WAFRuleGroup{{
		ID: "detect", Enabled: true, Operator: "AND", Action: policy.WAFActionBlock, StatusCode: http.StatusForbidden,
		Rules: []policy.WAFRequestRule{
			{Field: "PATH", Operator: "PREFIX", Value: "/admin"},
			{Field: "METHOD", Operator: "EQUALS", Value: http.MethodPost},
		},
	}}
	if err := handler.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "http://example.test/admin", nil)
	if err := handler.ServeHTTP(response, request, caddyhttp.Handler(&nextHandler{})); err != nil {
		t.Fatal(err)
	}
	match := response.Header().Get("X-Goveto-WAF-Match")
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d", response.Code)
	}
	if !strings.Contains(match, "rule[1]:PATH:PREFIX") || !strings.Contains(match, "rule[2]:METHOD:EQUALS") {
		t.Fatalf("AND group match detail missing rules in %q", match)
	}
}

func TestLocalAutoBanStoreThresholdBlocks(t *testing.T) {
	store := newLocalAutoBanStore()
	ctx := context.Background()
	cfg := policy.WAFAutoBan{Enabled: true, Hits: 3, WindowSeconds: 60, BanSeconds: 120, Scope: "SITE"}
	for range 2 {
		banned, _, err := store.RecordHit(ctx, "site", "g", "198.51.100.7", cfg)
		if err != nil {
			t.Fatal(err)
		}
		if banned {
			t.Fatal("banned before threshold")
		}
	}
	banned, retry, err := store.RecordHit(ctx, "site", "g", "198.51.100.7", cfg)
	if err != nil || !banned {
		t.Fatalf("threshold should ban: banned=%v err=%v", banned, err)
	}
	if retry <= 0 || retry > 120*time.Second {
		t.Fatalf("ban retry out of range: %v", retry)
	}
	blocked, _, err := store.Blocked(ctx, "site", "198.51.100.7")
	if err != nil || !blocked {
		t.Fatalf("client should be blocked after threshold: %v %v", blocked, err)
	}
	// Other clients are unaffected.
	blockedOther, _, _ := store.Blocked(ctx, "site", "198.51.100.8")
	if blockedOther {
		t.Fatal("unrelated IP was blocked")
	}
}

func TestLocalAutoBanDoesNotShortenExistingBan(t *testing.T) {
	store := newLocalAutoBanStore()
	ctx := context.Background()
	long := policy.WAFAutoBan{Enabled: true, Hits: 1, WindowSeconds: 60, BanSeconds: 300, Scope: "SITE"}
	short := policy.WAFAutoBan{Enabled: true, Hits: 1, WindowSeconds: 60, BanSeconds: 30, Scope: "SITE"}
	if _, _, err := store.RecordHit(ctx, "site", "long", "198.51.100.9", long); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.RecordHit(ctx, "site", "short", "198.51.100.9", short); err != nil {
		t.Fatal(err)
	}
	blocked, retry, err := store.Blocked(ctx, "site", "198.51.100.9")
	if err != nil || !blocked || retry < 290*time.Second {
		t.Fatalf("short ban replaced existing long ban: blocked=%v retry=%v err=%v", blocked, retry, err)
	}
}

func TestLocalAutoBanGlobalScopeIsSharedAcrossHandlers(t *testing.T) {
	t.Setenv("EDGE_AGENT_REDIS_URL", "")
	newHandler := func(siteID string) Handler {
		handler := Handler{SiteID: siteID, WAF: policy.DefaultWAFPolicy()}
		handler.WAF.Enabled = true
		handler.WAF.Presets = nil
		handler.WAF.Groups = []policy.WAFRuleGroup{{
			ID: "global", Enabled: true, Operator: "AND", Action: policy.WAFActionBlock, StatusCode: http.StatusForbidden,
			AutoBan: policy.WAFAutoBan{Enabled: true, Hits: 1, WindowSeconds: 60, BanSeconds: 300, Scope: "GLOBAL"},
			Rules:   []policy.WAFRequestRule{{Field: "PATH", Operator: "EQUALS", Value: "/attack"}},
		}}
		if err := handler.Provision(caddy.Context{}); err != nil {
			t.Fatal(err)
		}
		return handler
	}

	siteA := newHandler("global-a")
	siteB := newHandler("global-b")
	if siteA.autoBan != siteB.autoBan {
		t.Fatal("local auto-ban store is not shared across handlers")
	}
	ip := "192.0.2.240"
	if _, _, err := siteA.autoBan.RecordHit(context.Background(), siteA.SiteID, "global", ip, siteA.WAF.Groups[0].AutoBan); err != nil {
		t.Fatal(err)
	}
	blocked, _, err := siteB.autoBan.Blocked(context.Background(), siteB.SiteID, ip)
	if err != nil || !blocked {
		t.Fatalf("global ban from site A not visible to site B: blocked=%v err=%v", blocked, err)
	}
}

func TestAutoBanBlockValueMatchesTemporaryBlockSchema(t *testing.T) {
	now := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.FixedZone("test", 8*60*60))
	cfg := policy.WAFAutoBan{Enabled: true, Hits: 2, WindowSeconds: 60, BanSeconds: 300, Scope: "SITE"}
	encoded := autoBanBlockValue("site", "group", "198.51.100.7", cfg, now)
	var record securitystate.TemporaryBlock
	if err := json.Unmarshal([]byte(encoded), &record); err != nil {
		t.Fatalf("auto-ban block is not valid JSON: %v", err)
	}
	if record.Scope != "SITE" || record.SiteID != "site" || record.Address != "198.51.100.7" || record.CreatedBy != "waf:auto_ban" {
		t.Fatalf("unexpected auto-ban record: %#v", record)
	}
	if !record.ExpiresAt.Equal(record.CreatedAt.Add(300 * time.Second)) {
		t.Fatalf("unexpected auto-ban expiry: created=%v expires=%v", record.CreatedAt, record.ExpiresAt)
	}
}

func TestHandlerAutoBanBlocksAfterThresholdHits(t *testing.T) {
	handler := Handler{SiteID: "autoban", WAF: policy.DefaultWAFPolicy()}
	handler.WAF.Enabled = true
	handler.WAF.Presets = nil
	handler.WAF.Groups = []policy.WAFRuleGroup{{
		ID: "repeat", Enabled: true, Operator: "AND", Action: policy.WAFActionBlock, StatusCode: http.StatusForbidden,
		AutoBan: policy.WAFAutoBan{Enabled: true, Hits: 2, WindowSeconds: 60, BanSeconds: 300, Scope: "SITE"},
		Rules:   []policy.WAFRequestRule{{Field: "PATH", Operator: "EQUALS", Value: "/attack"}},
	}}
	if err := handler.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}
	if handler.autoBan == nil {
		t.Fatal("auto-ban store was not provisioned")
	}
	ip := "203.0.113.5"
	for i := range 2 {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://example.test/attack", nil)
		req.RemoteAddr = ip + ":1234"
		if err := handler.ServeHTTP(rec, req, caddyhttp.Handler(&nextHandler{})); err != nil {
			t.Fatal(err)
		}
		if rec.Code != http.StatusForbidden {
			t.Fatalf("hit %d status=%d (expected WAF block)", i+1, rec.Code)
		}
	}
	// Third request: previously accrued threshold from hit #2 triggers the ban,
	// so the client is blocked before the WAF even re-evaluates.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://example.test/attack", nil)
	req.RemoteAddr = ip + ":1234"
	if err := handler.ServeHTTP(rec, req, caddyhttp.Handler(&nextHandler{})); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusForbidden || rec.Header().Get("X-Goveto-WAF-Rule") != "waf:auto-ban" {
		t.Fatalf("auto-ban did not take over: status=%d rule=%q", rec.Code, rec.Header().Get("X-Goveto-WAF-Rule"))
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("Retry-After missing on auto-ban block")
	}
}

func TestRedisScriptMillisecondDuration(t *testing.T) {
	if got := redisScriptMillisecondDuration(300000); got != 300*time.Second {
		t.Fatalf("script ms conversion = %v, want 300s", got)
	}
	if got := redisScriptMillisecondDuration(0); got != 0 {
		t.Fatalf("zero ms = %v", got)
	}
	if got := redisScriptMillisecondDuration(-2); got != 0 {
		t.Fatalf("negative ms = %v", got)
	}
}

func TestAutoBanCountsHitActionWhitelist(t *testing.T) {
	for _, action := range []string{
		policy.WAFActionBlock, policy.WAFActionShowPage, policy.WAFActionRedirect, policy.WAFActionCaptcha,
	} {
		if !autoBanCountsHit(action, policy.WAFModeBlock) {
			t.Fatalf("enforcing action %s should count", action)
		}
	}
	for _, action := range []string{policy.WAFActionMonitor, policy.WAFActionTag, policy.WAFActionAllow} {
		if autoBanCountsHit(action, policy.WAFModeBlock) {
			t.Fatalf("soft action %s must not count", action)
		}
	}
	if autoBanCountsHit(policy.WAFActionBlock, policy.WAFModeMonitor) {
		t.Fatal("engine monitor mode must not count hits")
	}
}

func TestHandlerAutoBanSkippedInMonitorMode(t *testing.T) {
	handler := Handler{SiteID: "monitor", WAF: policy.DefaultWAFPolicy()}
	handler.WAF.Enabled = true
	handler.WAF.Mode = policy.WAFModeMonitor
	handler.WAF.Presets = nil
	handler.WAF.Groups = []policy.WAFRuleGroup{{
		ID: "repeat", Enabled: true, Operator: "AND", Action: policy.WAFActionBlock, StatusCode: http.StatusForbidden,
		AutoBan: policy.WAFAutoBan{Enabled: true, Hits: 1, WindowSeconds: 60, BanSeconds: 300, Scope: "SITE"},
		Rules:   []policy.WAFRequestRule{{Field: "PATH", Operator: "EQUALS", Value: "/attack"}},
	}}
	if err := handler.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}
	ip := "203.0.113.9"
	for range 3 {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://example.test/attack", nil)
		req.RemoteAddr = ip + ":1234"
		if err := handler.ServeHTTP(rec, req, caddyhttp.Handler(&nextHandler{})); err != nil {
			t.Fatal(err)
		}
		if rec.Code != http.StatusOK || rec.Header().Get("X-Goveto-WAF") != "MONITOR" {
			t.Fatalf("monitor mode leaked enforcement: status=%d waf=%q rule=%q",
				rec.Code, rec.Header().Get("X-Goveto-WAF"), rec.Header().Get("X-Goveto-WAF-Rule"))
		}
		if rec.Header().Get("X-Goveto-WAF-Rule") == "waf:auto-ban" {
			t.Fatal("auto-ban must not enforce while WAF mode is MONITOR")
		}
	}
	// Even if a ban were present in the store, monitor mode must not hard-block.
	_, _, _ = handler.autoBan.RecordHit(context.Background(), handler.SiteID, "repeat", ip,
		policy.WAFAutoBan{Enabled: true, Hits: 1, WindowSeconds: 60, BanSeconds: 300, Scope: "SITE"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	req.RemoteAddr = ip + ":1234"
	if err := handler.ServeHTTP(rec, req, caddyhttp.Handler(&nextHandler{})); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || rec.Header().Get("X-Goveto-WAF-Rule") == "waf:auto-ban" {
		t.Fatalf("stored ban enforced under monitor mode: status=%d rule=%q", rec.Code, rec.Header().Get("X-Goveto-WAF-Rule"))
	}
}

func TestLocalAutoBanStoreCapsDistinctCounters(t *testing.T) {
	store := newLocalAutoBanStore()
	ctx := context.Background()
	cfg := policy.WAFAutoBan{Enabled: true, Hits: 100, WindowSeconds: 60, BanSeconds: 120, Scope: "SITE"}
	for index := 0; index < localAutoBanMaxKeys; index++ {
		ip := fmt.Sprintf("198.51.100.%d", index%250+1)
		// Use unique group ids so keys stay distinct beyond the /24.
		_, _, err := store.RecordHit(ctx, "site", fmt.Sprintf("g-%d", index), ip, cfg)
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := len(store.counts); got > localAutoBanMaxKeys {
		t.Fatalf("counter map grew past cap: %d", got)
	}
	// One more distinct key must not expand the map beyond the cap.
	before := len(store.counts)
	_, _, _ = store.RecordHit(ctx, "site", "overflow", "203.0.113.50", cfg)
	if got := len(store.counts); got > localAutoBanMaxKeys {
		t.Fatalf("counter map exceeded cap after overflow hit: before=%d after=%d", before, got)
	}
}

func TestHandlerAutoBanLocalStoreIsolatedPerGroup(t *testing.T) {
	handler := Handler{SiteID: "iso", WAF: policy.DefaultWAFPolicy()}
	handler.WAF.Enabled = true
	handler.WAF.Presets = nil
	handler.WAF.Groups = []policy.WAFRuleGroup{
		{
			ID: "a", Enabled: true, Operator: "AND", Action: policy.WAFActionBlock, StatusCode: http.StatusForbidden,
			AutoBan: policy.WAFAutoBan{Enabled: true, Hits: 2, WindowSeconds: 60, BanSeconds: 300, Scope: "SITE"},
			Rules:   []policy.WAFRequestRule{{Field: "PATH", Operator: "EQUALS", Value: "/a"}},
		},
		{
			ID: "b", Enabled: true, Operator: "AND", Action: policy.WAFActionTag, Tag: "b-hit",
			Rules: []policy.WAFRequestRule{{Field: "PATH", Operator: "EQUALS", Value: "/b"}},
		},
	}
	if err := handler.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "http://example.test/a", nil)
	req.RemoteAddr = "198.18.0.1:1"
	_ = handler.ServeHTTP(httptest.NewRecorder(), req, caddyhttp.Handler(&nextHandler{}))
	// One hit against group a must not block group b's path on next request.
	rec := httptest.NewRecorder()
	reqB := httptest.NewRequest(http.MethodGet, "http://example.test/b", nil)
	reqB.RemoteAddr = "198.18.0.1:1"
	if err := handler.ServeHTTP(rec, reqB, caddyhttp.Handler(&nextHandler{})); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("unrelated group b should be reachable, got status=%d", rec.Code)
	}
}
