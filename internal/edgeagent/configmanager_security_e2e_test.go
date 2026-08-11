package edgeagent

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	securitypolicy "goveto-edge/internal/policy"
)

func TestAgentSecurityEndToEnd(t *testing.T) {
	ensureAgentLogSink(t)
	var originRequests atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originRequests.Add(1)
		_, _ = w.Write([]byte("origin:" + r.URL.Path))
	}))
	defer origin.Close()

	port := freePort(t)
	manager := NewConfigManager(filepath.Join(t.TempDir(), "sites.json"), ":"+strconv.Itoa(port))
	config := SiteConfig{
		SiteID: "secure-site", Version: 1, Domains: []string{"secure.example.test"},
		Listener: ListenerConfig{HTTPEnabled: true, HTTPPort: port},
		Origins:  []OriginConfig{{Protocol: "http", Address: strings.TrimPrefix(origin.URL, "http://")}},
	}
	waf := securitypolicy.WAFPolicy{Enabled: true, RuleSets: []securitypolicy.WAFRuleSet{{
		ID: "custom", Name: "Custom", Enabled: true,
		Rules: []securitypolicy.WAFRule{
			{ID: "monitor", Name: "Monitor", Enabled: true, Type: securitypolicy.WAFRuleTypeMatch, Conditions: securitypolicy.WAFConditions{Operator: "AND", Groups: []securitypolicy.WAFConditionGroup{{Operator: "AND", Conditions: []securitypolicy.WAFCondition{{Field: "HEADER", FieldName: "X-Monitor", Operator: "EQUALS", Value: "yes"}}}}}, Action: securitypolicy.WAFAction{Type: securitypolicy.WAFActionMonitor}},
			{ID: "blocked-header", Name: "Blocked header", Enabled: true, Type: securitypolicy.WAFRuleTypeMatch, Conditions: securitypolicy.WAFConditions{Operator: "AND", Groups: []securitypolicy.WAFConditionGroup{{Operator: "AND", Conditions: []securitypolicy.WAFCondition{{Field: "HEADER", FieldName: "X-Blocked", Operator: "EQUALS", Value: "yes"}}}}}, Action: securitypolicy.WAFAction{Type: securitypolicy.WAFActionBlock, StatusCode: http.StatusForbidden}},
			{ID: "login-cc", Name: "Login CC", Enabled: true, Type: securitypolicy.WAFRuleTypeRateLimit, Key: "CLIENT_IP_PATH", Requests: 2, WindowSeconds: 60, Action: securitypolicy.WAFAction{Type: securitypolicy.WAFActionShowPage, StatusCode: http.StatusTooManyRequests, Response: securitypolicy.WAFResponse{Type: securitypolicy.WAFResponseDefault}}},
		},
	}}}
	config.WAF = toMap(t, waf)
	if err := manager.ApplySite(config); err != nil {
		t.Fatal(err)
	}
	defer manager.Stop()

	blocked := requestEdge(t, port, config.Domains[0], http.MethodGet, "/custom", http.Header{"X-Blocked": {"yes"}})
	if blocked.status != http.StatusForbidden || blocked.header.Get("X-Goveto-WAF-Rule") != "blocked-header" {
		t.Fatalf("blocked response: status=%d headers=%v", blocked.status, blocked.header)
	}
	monitored := requestEdge(t, port, config.Domains[0], http.MethodGet, "/observed", http.Header{"X-Monitor": {"yes"}})
	if monitored.status != http.StatusOK || monitored.header.Get("X-Goveto-WAF") != securitypolicy.WAFActionMonitor {
		t.Fatalf("monitor response: status=%d headers=%v", monitored.status, monitored.header)
	}
	for index := 1; index <= 3; index++ {
		response := requestEdge(t, port, config.Domains[0], http.MethodGet, "/login", nil)
		if index <= 2 && response.status != http.StatusOK {
			t.Fatalf("CC request %d status=%d", index, response.status)
		}
		if index == 3 && (response.status != http.StatusTooManyRequests || response.header.Get("X-Goveto-WAF-Rule") != "login-cc") {
			t.Fatalf("third CC request: status=%d headers=%v", response.status, response.header)
		}
	}
	if originRequests.Load() != 3 {
		t.Fatalf("origin requests=%d, want monitor plus two allowed CC requests", originRequests.Load())
	}
}
