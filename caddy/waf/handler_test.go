package waf

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"

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

type fakeDistributedStore struct {
	mu           sync.Mutex
	counts       map[string]int
	challenges   map[string]bool
	allowCalls   int
	blockedCalls int
	blocked      bool
	err          error
	allowErr     error
	blockedErr   error
}

func (s *fakeDistributedStore) Allow(_ context.Context, siteID, ruleID, value string, rule policy.WAFRule) (bool, time.Duration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.allowCalls++
	if s.allowErr != nil {
		return false, 0, s.allowErr
	}
	if s.err != nil {
		return false, 0, s.err
	}
	if s.counts == nil {
		s.counts = map[string]int{}
	}
	key := siteID + "\x00" + ruleID + "\x00" + value
	s.counts[key]++
	return s.counts[key] <= rule.Requests+rule.Burst, time.Minute, nil
}

func (s *fakeDistributedStore) Blocked(context.Context, string, string) (bool, time.Duration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blockedCalls++
	if s.blockedErr != nil {
		return false, 0, s.blockedErr
	}
	return s.blocked, time.Minute, s.err
}

func (s *fakeDistributedStore) PutChallenge(_ context.Context, token string, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	if s.challenges == nil {
		s.challenges = map[string]bool{}
	}
	s.challenges[token] = true
	return nil
}

func (s *fakeDistributedStore) ConsumeChallenge(_ context.Context, token string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return false, s.err
	}
	if !s.challenges[token] {
		return false, nil
	}
	delete(s.challenges, token)
	return true, nil
}

func TestBuiltinsInspectDecodedQueryBodyAndPath(t *testing.T) {
	tests := []struct {
		name, method, target, body, rule string
	}{
		{"xss query", http.MethodGet, "/?q=%3Cscript%3Ealert(1)%3C/script%3E", "", "builtin-xss-query"},
		{"sql query", http.MethodGet, "/?id=1%20UNION%20SELECT%20password%20FROM%20users", "", "builtin-sql-query"},
		{"xss body", http.MethodPost, "/submit", "name=<svg onload=alert(1)>", "builtin-xss-body"},
		{"encoded traversal", http.MethodGet, "/a/%2e%2e/%2e%2e/secret", "", "builtin-path-traversal-target"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := provisionHandler(t, "builtin-"+test.name, policy.DefaultWAFPolicy())
			req := httptest.NewRequest(test.method, test.target, strings.NewReader(test.body))
			response := httptest.NewRecorder()
			next := &nextHandler{}
			if err := h.ServeHTTP(response, req, next); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusForbidden || response.Header().Get("X-Goveto-WAF-Rule") != test.rule || next.calls != 0 {
				t.Fatalf("status=%d rule=%q next=%d", response.Code, response.Header().Get("X-Goveto-WAF-Rule"), next.calls)
			}
		})
	}
}

func TestSensitiveDirectoryBuiltins(t *testing.T) {
	for _, path := range []string{"/.git/config", "/.svn/entries", "/.htaccess", "/.idea/workspace.xml", "/.env", "/.vscode/settings.json"} {
		h := provisionHandler(t, "sensitive-"+path, policy.DefaultWAFPolicy())
		response := httptest.NewRecorder()
		if err := h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil), &nextHandler{}); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusForbidden || response.Header().Get("X-Goveto-WAF-Rule") != "builtin-sensitive-directories-path" {
			t.Fatalf("path %q status=%d rule=%q", path, response.Code, response.Header().Get("X-Goveto-WAF-Rule"))
		}
	}
}

func TestRuleOrderingSoftActionsAndTerminalStop(t *testing.T) {
	match := func(id, action, tag string) policy.WAFRule {
		return policy.WAFRule{ID: id, Enabled: true, Type: policy.WAFRuleTypeMatch, Conditions: testConditions(policy.WAFCondition{Field: "PATH", Operator: "EQUALS", Value: "/hit"}), Action: policy.WAFAction{Type: action, Tag: tag, StatusCode: 451, Response: policy.WAFResponse{Type: policy.WAFResponseText, Body: "stopped"}}}
	}
	waf := policy.WAFPolicy{Enabled: true, RuleSets: []policy.WAFRuleSet{
		{ID: "soft", Enabled: true, Rules: []policy.WAFRule{match("monitor", policy.WAFActionMonitor, ""), match("tag-a", policy.WAFActionTag, "risk.a"), match("tag-b", policy.WAFActionTag, "risk.b")}},
		{ID: "terminal", Enabled: true, Rules: []policy.WAFRule{match("stop", policy.WAFActionShowPage, ""), match("never", policy.WAFActionAllow, "")}},
	}}
	h := provisionHandler(t, "ordering", waf)
	response := httptest.NewRecorder()
	next := &nextHandler{}
	if err := h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/hit", nil), next); err != nil {
		t.Fatal(err)
	}
	if response.Code != 451 || response.Header().Get("X-Goveto-WAF-Rule") != "stop" || next.calls != 0 {
		t.Fatalf("status=%d rule=%q next=%d", response.Code, response.Header().Get("X-Goveto-WAF-Rule"), next.calls)
	}
	if got := strings.Join(response.Header().Values("X-Goveto-WAF-Tags"), ","); got != "risk.a,risk.b" {
		t.Fatalf("tags=%q", got)
	}
}

func TestAllowIsTerminal(t *testing.T) {
	waf := singleRulePolicy(policy.WAFRule{ID: "allow", Enabled: true, Type: policy.WAFRuleTypeMatch, Conditions: testConditions(policy.WAFCondition{Field: "PATH", Operator: "PREFIX", Value: "/"}), Action: policy.WAFAction{Type: policy.WAFActionAllow}})
	h := provisionHandler(t, "allow", waf)
	response := httptest.NewRecorder()
	next := &nextHandler{}
	if err := h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil), next); err != nil {
		t.Fatal(err)
	}
	if next.calls != 1 || response.Header().Get("X-Goveto-WAF") != policy.WAFActionAllow {
		t.Fatalf("next=%d action=%q", next.calls, response.Header().Get("X-Goveto-WAF"))
	}
}

func TestCCDefaultAllowsEightyThenBlocks(t *testing.T) {
	waf := policy.DefaultWAFPolicy()
	waf.RuleSets = waf.RuleSets[4:]
	h := provisionHandler(t, "cc-default", waf)
	for index := 1; index <= 81; index++ {
		response := httptest.NewRecorder()
		next := &nextHandler{}
		req := httptest.NewRequest(http.MethodGet, "/asset", nil)
		req.RemoteAddr = "192.0.2.8:1234"
		if err := h.ServeHTTP(response, req, next); err != nil {
			t.Fatal(err)
		}
		if index <= 80 && (response.Code != http.StatusOK || next.calls != 1) {
			t.Fatalf("request %d unexpectedly blocked: %d", index, response.Code)
		}
		if index == 81 && (response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") == "") {
			t.Fatalf("request 81 status=%d retry=%q", response.Code, response.Header().Get("Retry-After"))
		}
	}
}

func TestDistributedRateSuccessDoesNotAdvanceLocalFallback(t *testing.T) {
	rule := policy.WAFRule{ID: "rate", Enabled: true, Type: policy.WAFRuleTypeRateLimit, Key: "GLOBAL", Requests: 1, WindowSeconds: 60, Backend: "REDIS", FailureMode: "LOCAL", Action: policy.WAFAction{Type: policy.WAFActionBlock, StatusCode: 429}}
	h := provisionHandler(t, "distributed", singleRulePolicy(rule))
	store := &fakeDistributedStore{}
	h.distributed, h.distributedErr = store, nil
	h.rateBackend = &rateBackendState{store: store}
	data := requestData{request: httptest.NewRequest(http.MethodGet, "/", nil), ip: "192.0.2.1"}
	if matched, _, _, err := h.matchRule(data, &h.ruleSets[0].rules[0]); err != nil || matched {
		t.Fatalf("distributed first request matched=%v err=%v", matched, err)
	}
	store.allowErr = context.DeadlineExceeded
	if matched, _, _, err := h.matchRule(data, &h.ruleSets[0].rules[0]); err != nil || matched {
		t.Fatalf("local fallback was already consumed: matched=%v err=%v", matched, err)
	}
}

func TestRateLimitRedisFailureModes(t *testing.T) {
	t.Setenv("EDGE_AGENT_REDIS_URL", "")
	for _, test := range []struct {
		mode string
		want int
	}{{"OPEN", http.StatusOK}, {"CLOSED", http.StatusServiceUnavailable}, {"LOCAL", http.StatusOK}} {
		t.Run(test.mode, func(t *testing.T) {
			rule := policy.WAFRule{ID: "rate", Enabled: true, Type: policy.WAFRuleTypeRateLimit, Key: "GLOBAL", Requests: 1, WindowSeconds: 60,
				Backend: "REDIS", FailureMode: test.mode, Action: policy.WAFAction{Type: policy.WAFActionBlock, StatusCode: http.StatusTooManyRequests}}
			h := provisionHandler(t, "failure-"+test.mode, singleRulePolicy(rule))
			response := httptest.NewRecorder()
			if err := h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil), &nextHandler{}); err != nil {
				t.Fatal(err)
			}
			if response.Code != test.want {
				t.Fatalf("status=%d want=%d", response.Code, test.want)
			}
			if test.mode == "CLOSED" && (response.Header().Get("X-Goveto-WAF") != "ERROR" || response.Header().Get("X-Goveto-WAF-Match") != "backend_unavailable") {
				t.Fatalf("closed failure event headers=%v", response.Header())
			}
		})
	}
}

func TestRedisRuntimeFailureIsCached(t *testing.T) {
	rule := policy.WAFRule{ID: "rate", Enabled: true, Type: policy.WAFRuleTypeRateLimit, Key: "GLOBAL", Requests: 1, WindowSeconds: 60,
		Backend: "REDIS", FailureMode: "OPEN", Action: policy.WAFAction{Type: policy.WAFActionBlock, StatusCode: http.StatusTooManyRequests}}
	h := provisionHandler(t, "runtime-failure", singleRulePolicy(rule))
	store := &fakeDistributedStore{allowErr: errors.New("redis unavailable")}
	h.distributed, h.distributedErr = store, nil
	h.rateBackend = &rateBackendState{store: store}
	compiled := &h.ruleSets[0].rules[0]
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	now := time.Now()
	for range 2 {
		allowed, _, err := h.allowRate(request, compiled, "global", now)
		if err != nil || !allowed {
			t.Fatalf("fail-open allowed=%v err=%v", allowed, err)
		}
	}
	if store.allowCalls != 1 {
		t.Fatalf("Redis was retried %d times after a cached runtime failure", store.allowCalls)
	}
	store.mu.Lock()
	store.allowErr = nil
	store.mu.Unlock()
	allowed, _, err := h.allowRate(request, compiled, "global", now.Add(2*time.Second))
	if err != nil || !allowed {
		t.Fatalf("recovery probe allowed=%v err=%v", allowed, err)
	}
	if store.allowCalls != 2 {
		t.Fatalf("recovery probe calls=%d", store.allowCalls)
	}
	h.rateBackend.mu.Lock()
	backendErr := h.rateBackend.err
	h.rateBackend.mu.Unlock()
	if backendErr != nil {
		t.Fatalf("backend remained unhealthy after successful probe: %v", backendErr)
	}
	_, _, _ = h.allowRate(request, compiled, "another", now.Add(2*time.Second))
	if store.allowCalls != 3 {
		t.Fatalf("healthy Redis was not used after recovery: calls=%d", store.allowCalls)
	}
}

func TestRateBackendBackoffAndSingleProbe(t *testing.T) {
	for _, test := range []struct {
		failures int
		want     time.Duration
	}{{1, time.Second}, {2, 2 * time.Second}, {5, 16 * time.Second}, {6, 30 * time.Second}, {20, 30 * time.Second}} {
		if got := rateBackendBackoff(test.failures); got != test.want {
			t.Fatalf("failures=%d backoff=%s want=%s", test.failures, got, test.want)
		}
	}

	now := time.Now()
	h := &Handler{rateBackend: &rateBackendState{err: errors.New("unavailable"), retryAt: now.Add(-time.Second)}}
	_, _, probe, _ := h.acquireRateBackend(now)
	if !probe {
		t.Fatal("first request after retry deadline did not enter half-open probe")
	}
	_, _, probe, _ = h.acquireRateBackend(now)
	if probe {
		t.Fatal("second request entered half-open probe while one was already active")
	}
}

func TestRateLimitFailureDoesNotContaminateTemporaryBlocks(t *testing.T) {
	rule := policy.WAFRule{ID: "rate", Enabled: true, Type: policy.WAFRuleTypeRateLimit, Key: "GLOBAL", Requests: 1, WindowSeconds: 60,
		Backend: "REDIS", FailureMode: "OPEN", Action: policy.WAFAction{Type: policy.WAFActionBlock, StatusCode: http.StatusTooManyRequests}}
	access := policy.DefaultAccessPolicy()
	access.Enabled, access.TemporaryBlocks = true, true
	access.TemporaryBlockFailure = "CLOSED"
	h := &Handler{SiteID: "isolated-backends", WAF: singleRulePolicy(rule), Access: access}
	if err := h.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}
	store := &fakeDistributedStore{allowErr: errors.New("rate Redis unavailable")}
	h.distributed, h.distributedErr = store, nil
	h.rateBackend = &rateBackendState{store: store}

	response := httptest.NewRecorder()
	if err := h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil), &nextHandler{}); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK {
		t.Fatalf("rate failure contaminated temporary blocks: status=%d", response.Code)
	}
	if store.blockedCalls != 1 || store.allowCalls != 1 {
		t.Fatalf("blocked calls=%d allow calls=%d", store.blockedCalls, store.allowCalls)
	}
}

func TestTemporaryBlockBackendDecision(t *testing.T) {
	access := policy.DefaultAccessPolicy()
	access.Enabled, access.TemporaryBlocks = true, true
	h := &Handler{SiteID: "blocks", WAF: policy.WAFPolicy{}, Access: access}
	if err := h.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}
	h.distributed, h.distributedErr = &fakeDistributedStore{blocked: true}, nil
	response := httptest.NewRecorder()
	if err := h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil), &nextHandler{}); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusForbidden || response.Header().Get("X-Goveto-WAF-Match") != "temporary_block" {
		t.Fatalf("temporary block status=%d headers=%v", response.Code, response.Header())
	}
	h.distributed = &fakeDistributedStore{err: errors.New("redis unavailable")}
	h.Access.TemporaryBlockFailure = "OPEN"
	response = httptest.NewRecorder()
	if err := h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil), &nextHandler{}); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK {
		t.Fatalf("fail-open temporary block status=%d", response.Code)
	}
}

func TestAccessPolicyUsesOnlyTrustedProxyChain(t *testing.T) {
	access := policy.DefaultAccessPolicy()
	access.Enabled = true
	access.TrustedProxies = []string{"10.0.0.0/8"}
	access.IPBlocklist = []string{"198.51.100.0/24"}
	h := &Handler{SiteID: "access", WAF: policy.WAFPolicy{}, Access: access}
	if err := h.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "10.0.0.2:1234"
	request.Header.Set("X-Forwarded-For", "203.0.113.9, 198.51.100.8")
	response := httptest.NewRecorder()
	if err := h.ServeHTTP(response, request, &nextHandler{}); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusForbidden || response.Header().Get("X-Goveto-WAF-Rule") != "access:ip-blocklist" {
		t.Fatalf("trusted proxy decision status=%d headers=%v", response.Code, response.Header())
	}
	request = httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "192.0.2.2:1234"
	request.Header.Set("X-Forwarded-For", "198.51.100.8")
	response = httptest.NewRecorder()
	if err := h.ServeHTTP(response, request, &nextHandler{}); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK {
		t.Fatalf("untrusted client spoofed XFF: status=%d", response.Code)
	}
}

func TestHandlerResponseRedirectAndTagActions(t *testing.T) {
	makeRule := func(id, path string, action policy.WAFAction) policy.WAFRule {
		return policy.WAFRule{ID: id, Enabled: true, Type: policy.WAFRuleTypeMatch,
			Conditions: testConditions(policy.WAFCondition{Field: "PATH", Operator: "EQUALS", Value: path}), Action: action}
	}
	waf := policy.WAFPolicy{Enabled: true, RuleSets: []policy.WAFRuleSet{{ID: "actions", Enabled: true, Rules: []policy.WAFRule{
		makeRule("page", "/page", policy.WAFAction{Type: policy.WAFActionShowPage, StatusCode: 451, Response: policy.WAFResponse{Type: policy.WAFResponseHTML, Body: "<h1>Unavailable here</h1>"}}),
		makeRule("redirect", "/old", policy.WAFAction{Type: policy.WAFActionRedirect, RedirectURL: "/safe", RedirectStatus: http.StatusTemporaryRedirect}),
		makeRule("tag", "/tag", policy.WAFAction{Type: policy.WAFActionTag, Tag: "trusted.bot"}),
	}}}}
	h := provisionHandler(t, "actions", waf)
	page := httptest.NewRecorder()
	if err := h.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/page", nil), &nextHandler{}); err != nil {
		t.Fatal(err)
	}
	if page.Code != 451 || page.Body.String() != "<h1>Unavailable here</h1>" {
		t.Fatalf("custom page status=%d body=%q", page.Code, page.Body.String())
	}
	redirect := httptest.NewRecorder()
	if err := h.ServeHTTP(redirect, httptest.NewRequest(http.MethodGet, "/old", nil), &nextHandler{}); err != nil {
		t.Fatal(err)
	}
	if redirect.Code != http.StatusTemporaryRedirect || redirect.Header().Get("Location") != "/safe" {
		t.Fatalf("redirect status=%d location=%q", redirect.Code, redirect.Header().Get("Location"))
	}
	next := &nextHandler{}
	tagged := httptest.NewRecorder()
	if err := h.ServeHTTP(tagged, httptest.NewRequest(http.MethodGet, "/tag", nil), next); err != nil {
		t.Fatal(err)
	}
	if tagged.Code != http.StatusOK || next.tags != "trusted.bot" {
		t.Fatalf("tag status=%d upstream=%q", tagged.Code, next.tags)
	}
}

func TestLocalCounterIsBounded(t *testing.T) {
	store := &counterStore{entries: map[string]counter{}}
	rule := policy.WAFRule{Requests: 1, WindowSeconds: 60}
	now := time.Now()
	for index := 0; index < maxLocalRateKeys+100; index++ {
		store.allow(strings.Repeat("x", index%8)+time.Duration(index).String(), now.Add(time.Duration(index)*time.Millisecond), rule)
	}
	if len(store.entries) > maxLocalRateKeys {
		t.Fatalf("local counter contains %d keys", len(store.entries))
	}
}

func provisionHandler(t *testing.T, siteID string, waf policy.WAFPolicy) *Handler {
	t.Helper()
	h := &Handler{SiteID: siteID, WAF: waf, Access: policy.DefaultAccessPolicy()}
	if err := h.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}
	return h
}

func singleRulePolicy(rule policy.WAFRule) policy.WAFPolicy {
	return policy.WAFPolicy{Enabled: true, RuleSets: []policy.WAFRuleSet{{ID: "set", Enabled: true, Rules: []policy.WAFRule{rule}}}}
}

func testConditions(condition policy.WAFCondition) policy.WAFConditions {
	return policy.WAFConditions{Operator: "AND", Groups: []policy.WAFConditionGroup{{Operator: "AND", Conditions: []policy.WAFCondition{condition}}}}
}

func TestCompoundConditionsAndOr(t *testing.T) {
	rule := policy.WAFRule{
		ID: "compound", Enabled: true, Type: policy.WAFRuleTypeMatch,
		Conditions: policy.WAFConditions{Operator: "AND", Groups: []policy.WAFConditionGroup{
			{Operator: "AND", Conditions: []policy.WAFCondition{{Field: "METHOD", Operator: "EQUALS", Value: "POST"}}},
			{Operator: "OR", Conditions: []policy.WAFCondition{{Field: "PATH", Operator: "EQUALS", Value: "/login"}, {Field: "PATH", Operator: "EQUALS", Value: "/register"}}},
		}},
		Action: policy.WAFAction{Type: policy.WAFActionBlock, StatusCode: http.StatusForbidden},
	}
	h := provisionHandler(t, "compound", singleRulePolicy(rule))
	for _, test := range []struct {
		method, path string
		blocked      bool
	}{{http.MethodPost, "/login", true}, {http.MethodPost, "/register", true}, {http.MethodGet, "/login", false}, {http.MethodPost, "/other", false}} {
		response := httptest.NewRecorder()
		next := &nextHandler{}
		if err := h.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil), next); err != nil {
			t.Fatal(err)
		}
		if (response.Code == http.StatusForbidden) != test.blocked {
			t.Fatalf("%s %s status=%d blocked=%v", test.method, test.path, response.Code, test.blocked)
		}
	}
}

func TestRateLimitConditionsFilterTraffic(t *testing.T) {
	rule := policy.WAFRule{ID: "login-rate", Enabled: true, Type: policy.WAFRuleTypeRateLimit, Key: "GLOBAL", Requests: 1, WindowSeconds: 60,
		Conditions: testConditions(policy.WAFCondition{Field: "PATH", Operator: "EQUALS", Value: "/login"}),
		Action:     policy.WAFAction{Type: policy.WAFActionBlock, StatusCode: http.StatusTooManyRequests}}
	h := provisionHandler(t, "conditional-rate", singleRulePolicy(rule))
	for index, path := range []string{"/other", "/login", "/other", "/login"} {
		response := httptest.NewRecorder()
		if err := h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil), &nextHandler{}); err != nil {
			t.Fatal(err)
		}
		if path == "/other" && response.Code != http.StatusOK {
			t.Fatalf("unmatched traffic was limited: %d", response.Code)
		}
		if path == "/login" {
			want := http.StatusOK
			if index == 3 {
				want = http.StatusTooManyRequests
			}
			if response.Code != want {
				t.Fatalf("login request %d status=%d want=%d", index, response.Code, want)
			}
		}
	}
}

func captchaPolicy() policy.WAFPolicy {
	return singleRulePolicy(policy.WAFRule{
		ID: "shield", Enabled: true, Type: policy.WAFRuleTypeMatch,
		Conditions: testConditions(policy.WAFCondition{Field: "PATH", Operator: "EQUALS", Value: "/protected"}),
		Action:     policy.WAFAction{Type: policy.WAFActionCaptcha},
	})
}

func TestProofOfWorkCaptchaGrantsClearance(t *testing.T) {
	h := &Handler{SiteID: "captcha-site", ChallengeSecret: testChallengeSecret(), WAF: captchaPolicy(), Access: policy.DefaultAccessPolicy()}
	if err := h.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}
	ip := "198.51.100.20"
	base := captchaRequest("http://example.test/protected", ip)
	token, err := h.challengeToken("shield", base, ip)
	if err != nil {
		t.Fatal(err)
	}
	proof := solveChallenge(t, token, testBrowserEnvironment(base))
	verify := captchaRequest("http://example.test/protected?__goveto_challenge="+url.QueryEscape(token)+"&__goveto_proof="+proof, ip)
	verified := httptest.NewRecorder()
	if err = h.ServeHTTP(verified, verify, &nextHandler{}); err != nil {
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
		if err = h.ServeHTTP(response, request, next); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusOK || next.calls != 1 || response.Header().Get("X-Goveto-WAF") != "CAPTCHA-PASS" {
			t.Fatalf("clearance attempt=%d status=%d calls=%d", attempt+1, response.Code, next.calls)
		}
	}
}

func TestDistributedChallengeStateRejectsReplay(t *testing.T) {
	h := &Handler{SiteID: "captcha-replay", ChallengeSecret: testChallengeSecret(), WAF: captchaPolicy(), Access: policy.DefaultAccessPolicy()}
	if err := h.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}
	store := &fakeDistributedStore{}
	h.distributed, h.distributedErr = store, nil
	ip := "198.51.100.21"
	base := captchaRequest("http://example.test/protected", ip)
	token, err := h.challengeToken("shield", base, ip)
	if err != nil {
		t.Fatal(err)
	}
	claim, ok := h.verifyClaim(token)
	if !ok || !claim.Stateful || claim.RuleID != "shield" {
		t.Fatalf("unexpected challenge claim: %#v", claim)
	}
	proof := solveChallenge(t, token, testBrowserEnvironment(base))
	target := "http://example.test/protected?__goveto_challenge=" + url.QueryEscape(token) + "&__goveto_proof=" + proof
	store.mu.Lock()
	store.err = errors.New("redis unavailable")
	store.mu.Unlock()
	backendFailure := httptest.NewRecorder()
	if err = h.ServeHTTP(backendFailure, captchaRequest(target, ip), &nextHandler{}); err != nil {
		t.Fatal(err)
	}
	if backendFailure.Code != http.StatusServiceUnavailable || backendFailure.Header().Get("X-Goveto-WAF-Challenge") != "backend_unavailable" || len(backendFailure.Result().Cookies()) != 0 {
		t.Fatalf("backend failure status=%d headers=%v cookies=%v", backendFailure.Code, backendFailure.Header(), backendFailure.Result().Cookies())
	}
	store.mu.Lock()
	store.err = nil
	store.mu.Unlock()
	first := httptest.NewRecorder()
	if err = h.ServeHTTP(first, captchaRequest(target, ip), &nextHandler{}); err != nil {
		t.Fatal(err)
	}
	if first.Code != http.StatusSeeOther {
		t.Fatalf("first completion status=%d", first.Code)
	}
	replay := httptest.NewRecorder()
	if err = h.ServeHTTP(replay, captchaRequest(target, ip), &nextHandler{}); err != nil {
		t.Fatal(err)
	}
	if replay.Code != http.StatusServiceUnavailable || replay.Header().Get("X-Goveto-WAF-Challenge") != "replayed" {
		t.Fatalf("replay status=%d headers=%v", replay.Code, replay.Header())
	}
}

func TestCaptchaPageEmbedsVersionedWorkerSolver(t *testing.T) {
	h := &Handler{SiteID: "captcha-page", ChallengeSecret: testChallengeSecret(), WAF: captchaPolicy(), Access: policy.DefaultAccessPolicy()}
	if err := h.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	request := captchaRequest("http://example.test/protected", "192.0.2.1")
	if err := h.ServeHTTP(response, request, &nextHandler{}); err != nil {
		t.Fatal(err)
	}
	body := response.Body.String()
	if response.Code != http.StatusServiceUnavailable || strings.Contains(body, "ZgotmplZ") || !strings.Contains(body, "new Worker") || !strings.Contains(body, "Scrypt implementation bundled") {
		t.Fatalf("CAPTCHA worker page status=%d body=%q", response.Code, body)
	}
	if csp := response.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "img-src data:") {
		t.Fatalf("CAPTCHA CSP=%q", csp)
	}
}

func TestProofOfWorkRejectsTamperingBindingAndOutOfRangeProof(t *testing.T) {
	h := &Handler{SiteID: "captcha-security", ChallengeSecret: testChallengeSecret(), WAF: captchaPolicy(), Access: policy.DefaultAccessPolicy()}
	if err := h.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}
	base := captchaRequest("http://example.test/protected", "198.51.100.20")
	token, err := h.challengeToken("shield", base, "198.51.100.20")
	if err != nil {
		t.Fatal(err)
	}
	proof := solveChallenge(t, token, testBrowserEnvironment(base))
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
		name, token, proof, ip string
	}{
		{"tampered token", tamperToken(token), proof, "198.51.100.20"},
		{"non-canonical token", nonCanonicalToken(token), proof, "198.51.100.20"},
		{"wrong IP", token, proof, "198.51.100.21"},
		{"out of range", token, outOfRangeProof, "198.51.100.20"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := captchaRequest("http://example.test/protected?__goveto_challenge="+url.QueryEscape(test.token)+"&__goveto_proof="+test.proof, test.ip)
			response := httptest.NewRecorder()
			if err := h.ServeHTTP(response, request, &nextHandler{}); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusServiceUnavailable || len(response.Result().Cookies()) != 0 {
				t.Fatalf("invalid proof accepted: status=%d", response.Code)
			}
		})
	}
}

func TestCaptchaClearanceWorksAcrossHandlersWithSharedSecret(t *testing.T) {
	newHandler := func() *Handler {
		h := &Handler{SiteID: "shared-site", ChallengeSecret: testChallengeSecret(), WAF: captchaPolicy(), Access: policy.DefaultAccessPolicy()}
		if err := h.Provision(caddy.Context{}); err != nil {
			t.Fatal(err)
		}
		return h
	}
	issuer, verifier := newHandler(), newHandler()
	request := captchaRequest("http://example.test/protected", "192.0.2.10")
	token, err := issuer.challengeToken("shield", request, "192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}
	claim, ok := verifier.verifyClaim(token)
	assessment, valid := verifier.validChallengeClaim(claim, request, "shield", "192.0.2.10", solveChallenge(t, token, testBrowserEnvironment(request)))
	if !ok || !valid || !assessment.Accepted || claim.RuleID != "shield" {
		t.Fatal("shared challenge secret did not work across handlers")
	}
}

func TestCaptchaRequiresPublishedChallengeSecret(t *testing.T) {
	h := &Handler{SiteID: "missing-secret", WAF: captchaPolicy(), Access: policy.DefaultAccessPolicy()}
	if err := h.Provision(caddy.Context{}); err == nil {
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
			encoded, encodeErr := encodeChallengeSolution(challengeSolution{Counter: counter, Key: base64.RawURLEncoding.EncodeToString(key), Environment: environment})
			if encodeErr != nil {
				t.Fatal(encodeErr)
			}
			return encoded
		}
	}
	t.Fatal("deterministic challenge had no solution")
	return ""
}
