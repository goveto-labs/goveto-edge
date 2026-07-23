package waf

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"

	"goveto-edge/internal/policy"
)

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
		Rules: []policy.WAFRequestRule{{Field: "PATH", Operator: "EQUALS", Value: "/protected"}},
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

	request := captchaRequest("http://example.test/protected", ip)
	request.AddCookie(result.Cookies()[0])
	response := httptest.NewRecorder()
	next := &nextHandler{}
	if err = handler.ServeHTTP(response, request, caddyhttp.Handler(next)); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || next.calls != 1 || response.Header().Get("X-Goveto-WAF") != "CAPTCHA-PASS" {
		t.Fatalf("clearance status=%d calls=%d header=%q", response.Code, next.calls, response.Header().Get("X-Goveto-WAF"))
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
