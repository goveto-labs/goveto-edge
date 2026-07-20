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
	manager.SetAgentHost("node-id")
	config := SiteConfig{
		SiteID:   "secure-site",
		Version:  1,
		Domains:  []string{"secure.example.test"},
		Listener: ListenerConfig{HTTPEnabled: true, HTTPPort: port},
		Origins:  []OriginConfig{{Protocol: "http", Address: strings.TrimPrefix(origin.URL, "http://")}},
	}
	waf := securitypolicy.DefaultWAFPolicy()
	waf.Enabled = true
	waf.Groups = []securitypolicy.WAFRuleGroup{{
		ID:       "blocked-header",
		Enabled:  true,
		Operator: "AND",
		Action:   "BLOCK",
		Rules: []securitypolicy.WAFRequestRule{
			{Field: "HEADER", Name: "X-Blocked", Operator: "EQUALS", Value: "yes"},
		},
	}}
	config.WAF = toMap(t, waf)
	if err := manager.ApplySite(config); err != nil {
		t.Fatal(err)
	}
	defer manager.Stop()

	t.Run("managed presets and custom groups block before origin", func(t *testing.T) {
		sqli := requestEdge(t, port, config.Domains[0], http.MethodGet, "/?id=1%20UNION%20SELECT%20password%20FROM%20users", nil)
		if sqli.status != http.StatusForbidden || sqli.header.Get("X-Goveto-WAF-Rule") != "preset:SQL_INJECTION" {
			t.Fatalf("SQL injection was not blocked: status=%d headers=%v", sqli.status, sqli.header)
		}
		custom := requestEdge(t, port, config.Domains[0], http.MethodGet, "/custom", http.Header{"X-Blocked": {"yes"}})
		if custom.status != http.StatusForbidden || custom.header.Get("X-Goveto-WAF-Rule") != "blocked-header" {
			t.Fatalf("custom WAF group was not blocked: status=%d headers=%v", custom.status, custom.header)
		}
		if originRequests.Load() != 0 {
			t.Fatalf("blocked requests reached origin: %d", originRequests.Load())
		}
	})

	t.Run("monitor mode records match and allows request", func(t *testing.T) {
		config.Version++
		waf.Mode = "MONITOR"
		config.WAF = toMap(t, waf)
		if err := manager.ApplySite(config); err != nil {
			t.Fatal(err)
		}
		response := requestEdge(t, port, config.Domains[0], http.MethodGet, "/custom", http.Header{"X-Blocked": {"yes"}})
		if response.status != http.StatusOK || response.header.Get("X-Goveto-WAF") != "MONITOR" {
			t.Fatalf("monitor mode response: status=%d headers=%v", response.status, response.header)
		}
		if originRequests.Load() != 1 {
			t.Fatalf("monitor request did not reach origin: %d", originRequests.Load())
		}
	})

	t.Run("CC group limits matching traffic", func(t *testing.T) {
		config.Version++
		waf.Enabled = false
		config.WAF = toMap(t, waf)
		config.RateLimit = toMap(t, securitypolicy.RateLimitPolicy{
			Enabled: true,
			Rules: []securitypolicy.RateLimitRule{
				{
					ID: "login-cc", Enabled: true, Key: "CLIENT_IP_PATH", Requests: 2, WindowSeconds: 60,
					Conditions: securitypolicy.RequestConditions{
						Groups: []securitypolicy.RequestConditionGroup{
							{
								Operator: "AND",
								Rules: []securitypolicy.WAFRequestRule{
									{Field: "PATH", Operator: "EQUALS", Value: "/login"},
								},
							},
						},
					},
				},
			},
		})
		if err := manager.ApplySite(config); err != nil {
			t.Fatal(err)
		}

		for index := 0; index < 3; index++ {
			response := requestEdge(t, port, config.Domains[0], http.MethodGet, "/login", nil)
			if index < 2 && response.status != http.StatusOK {
				t.Fatalf("request %d status=%d", index+1, response.status)
			}
			if index == 2 && (response.status != http.StatusTooManyRequests || response.header.Get("X-Goveto-WAF-Rule") != "login-cc") {
				t.Fatalf("third CC request was not limited: status=%d headers=%v", response.status, response.header)
			}
		}
		if originRequests.Load() != 3 {
			t.Fatalf("rate-limited request reached origin: total=%d", originRequests.Load())
		}
	})
}
