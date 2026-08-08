package policy

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDefaultSecurityPoliciesSerializeEmptyCollectionsAsArrays(t *testing.T) {
	waf := DefaultWAFPolicy()
	if !waf.Enabled {
		t.Fatal("WAF must be enabled by default")
	}
	value := struct {
		WAF       WAFPolicy       `json:"waf"`
		Access    AccessPolicy    `json:"access"`
		RateLimit RateLimitPolicy `json:"rate_limit"`
	}{WAF: waf, Access: DefaultAccessPolicy(), RateLimit: DefaultRateLimitPolicy()}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, expected := range []string{`"groups":[]`, `"exceptions":[]`, `"rules":[]`, `"ip_allowlist":[]`} {
		if !strings.Contains(text, expected) {
			t.Fatalf("default security JSON missing %s: %s", expected, text)
		}
	}
}

func TestAccessPolicyNormalizesNetworkAndRequestControls(t *testing.T) {
	policy := DefaultAccessPolicy()
	policy.Enabled = true
	policy.TrustedProxies = []string{"10.0.0.1", "10.0.0.0/8"}
	policy.IPBlocklist = []string{"192.0.2.4"}
	policy.AllowedMethods = []string{"get", "HEAD"}
	policy.AllowedRefererHosts = []string{"HTTPS://EXAMPLE.COM/path", "*.static.example"}
	if err := policy.NormalizeAndValidate(); err != nil {
		t.Fatal(err)
	}
	if policy.IPBlocklist[0] != "192.0.2.4/32" || policy.AllowedMethods[0] != "GET" || policy.AllowedRefererHosts[0] != "*.static.example" {
		t.Fatalf("access policy was not normalized: %#v", policy)
	}
}

func TestAccessPolicyPublicNormalizationIgnoresGeoIPPath(t *testing.T) {
	policy := DefaultAccessPolicy()
	policy.AllowedCountries = []string{"us"}
	policy.GeoIPDatabase = "/client/controlled.mmdb"
	if err := policy.NormalizeAndValidatePublic(); err != nil {
		t.Fatal(err)
	}
	if policy.GeoIPDatabase != "" || len(policy.AllowedCountries) != 1 || policy.AllowedCountries[0] != "US" {
		t.Fatalf("unexpected normalized access policy: %#v", policy)
	}
}

func TestVersionedWAFExceptionsAndDistributedFailureModeValidate(t *testing.T) {
	waf := DefaultWAFPolicy()
	waf.Exceptions = []WAFException{{Enabled: true, RuleIDs: []string{"preset:XSS"}}}
	if err := waf.NormalizeAndValidate(); err != nil {
		t.Fatal(err)
	}
	if waf.RuleSetVersion != CurrentWAFRuleSetVersion || waf.Exceptions[0].ID == "" {
		t.Fatalf("WAF version or exception was not normalized: %#v", waf)
	}
	rate := RateLimitPolicy{Backend: "redis", FailureMode: "closed"}
	if err := rate.NormalizeAndValidate(); err != nil {
		t.Fatal(err)
	}
	if rate.Backend != "REDIS" || rate.FailureMode != "CLOSED" {
		t.Fatalf("rate backend was not normalized: %#v", rate)
	}
}

func TestRateLimitSupportsPathCounter(t *testing.T) {
	policy := DefaultRateLimitPolicy()
	policy.Rules = []RateLimitRule{{
		ID: "path", Enabled: true, Key: "PATH", Requests: 10, WindowSeconds: 60,
	}}
	if err := policy.NormalizeAndValidate(); err != nil {
		t.Fatal(err)
	}
}

func TestWAFPolicyNormalizesComplexGroups(t *testing.T) {
	policy := DefaultWAFPolicy()
	policy.Enabled = true
	policy.Groups = []WAFRuleGroup{{
		Name:     "Admin protection",
		Enabled:  true,
		Operator: "and",
		Action:   "block",
		Rules: []WAFRequestRule{
			{Field: "path", Operator: "prefix", Value: "/admin"},
			{Field: "client_ip", Operator: "cidr", Values: []string{"192.0.2.0/24"}, Negate: true},
		},
	}}
	if err := policy.NormalizeAndValidate(); err != nil {
		t.Fatal(err)
	}
	if policy.Groups[0].ID != "group-1" || policy.Groups[0].Operator != "AND" {
		t.Fatalf("policy was not normalized: %#v", policy.Groups[0])
	}
}

func TestWAFPolicyRejectsInvalidRegexAndCIDR(t *testing.T) {
	for _, rule := range []WAFRequestRule{
		{Field: "PATH", Operator: "REGEX", Value: "["},
		{Field: "CLIENT_IP", Operator: "CIDR", Values: []string{"bad"}},
	} {
		policy := DefaultWAFPolicy()
		policy.Groups = []WAFRuleGroup{{Enabled: true, Operator: "AND", Rules: []WAFRequestRule{rule}}}
		if err := policy.NormalizeAndValidate(); err == nil {
			t.Fatalf("expected rule to be rejected: %#v", rule)
		}
	}
}

func TestWAFPolicyValidatesActionsAndCustomResponses(t *testing.T) {
	policy := DefaultWAFPolicy()
	policy.BlockResponse = WAFResponse{Type: WAFResponseJSON, Body: `{"error":"blocked"}`}
	policy.Groups = []WAFRuleGroup{
		{Enabled: true, Operator: "AND", Action: WAFActionShowPage, Response: WAFResponse{Type: WAFResponseHTML, Body: "<h1>Denied</h1>"}, Rules: []WAFRequestRule{{Field: "PATH", Operator: "EQUALS", Value: "/page"}}},
		{Enabled: true, Operator: "AND", Action: WAFActionRedirect, RedirectURL: "/login", Rules: []WAFRequestRule{{Field: "PATH", Operator: "EQUALS", Value: "/redirect"}}},
		{Enabled: true, Operator: "AND", Action: WAFActionTag, Tag: "risk.high", Rules: []WAFRequestRule{{Field: "PATH", Operator: "EQUALS", Value: "/tag"}}},
	}
	if err := policy.NormalizeAndValidate(); err != nil {
		t.Fatal(err)
	}
	if policy.Groups[1].RedirectStatus != 302 || policy.Groups[0].Response.Type != WAFResponseHTML {
		t.Fatalf("action defaults were not normalized: %#v", policy.Groups)
	}
}

func TestWAFPolicyRejectsUnsafeActionConfiguration(t *testing.T) {
	for _, group := range []WAFRuleGroup{
		{Enabled: true, Operator: "AND", Action: WAFActionRedirect, RedirectURL: "javascript:alert(1)"},
		{Enabled: true, Operator: "AND", Action: WAFActionTag, Tag: "bad tag"},
		{Enabled: true, Operator: "AND", Action: WAFActionShowPage, Response: WAFResponse{Type: WAFResponseJSON, Body: "not-json"}},
	} {
		group.Rules = []WAFRequestRule{{Field: "PATH", Operator: "EQUALS", Value: "/"}}
		policy := DefaultWAFPolicy()
		policy.Groups = []WAFRuleGroup{group}
		if err := policy.NormalizeAndValidate(); err == nil {
			t.Fatalf("expected action configuration to be rejected: %#v", group)
		}
	}
}

func TestRateLimitPolicyValidatesCCRule(t *testing.T) {
	policy := RateLimitPolicy{Enabled: true, Rules: []RateLimitRule{{
		Enabled:       true,
		Name:          "Login CC",
		Key:           "client_ip_path",
		Requests:      10,
		WindowSeconds: 60,
		Burst:         5,
		BanSeconds:    120,
		Conditions: RequestConditions{Groups: []RequestConditionGroup{{
			Operator: "AND",
			Rules:    []WAFRequestRule{{Field: "PATH", Operator: "EQUALS", Value: "/login"}},
		}}},
	}}}
	if err := policy.NormalizeAndValidate(); err != nil {
		t.Fatal(err)
	}
	if policy.Rules[0].ID != "cc-1" || policy.Rules[0].StatusCode != 429 {
		t.Fatalf("rate-limit policy was not normalized: %#v", policy.Rules[0])
	}
}

func TestWAFAutoBanValidation(t *testing.T) {
	for _, mutate := range []func(*WAFAutoBan){
		func(b *WAFAutoBan) { b.Hits = 0 },
		func(b *WAFAutoBan) { b.Hits = 100001 },
		func(b *WAFAutoBan) { b.WindowSeconds = 0 },
		func(b *WAFAutoBan) { b.BanSeconds = 0 },
		func(b *WAFAutoBan) { b.Scope = "PLANET" },
	} {
		policy := DefaultWAFPolicy()
		policy.Groups = []WAFRuleGroup{{
			ID: "g", Enabled: true, Operator: "AND", Action: WAFActionBlock,
			AutoBan: WAFAutoBan{Enabled: true, Hits: 3, WindowSeconds: 60, BanSeconds: 300, Scope: "SITE"},
			Rules:   []WAFRequestRule{{Field: "PATH", Operator: "EQUALS", Value: "/x"}},
		}}
		mutate(&policy.Groups[0].AutoBan)
		if err := policy.NormalizeAndValidate(); err == nil {
			t.Fatalf("expected auto-ban validation error for %#v", policy.Groups[0].AutoBan)
		}
	}
}

func TestWAFAutoBanDisabledIsDefaulted(t *testing.T) {
	policy := DefaultWAFPolicy()
	policy.Groups = []WAFRuleGroup{{
		ID: "g", Enabled: true, Operator: "AND", Action: WAFActionBlock,
		Rules: []WAFRequestRule{{Field: "PATH", Operator: "EQUALS", Value: "/x"}},
	}}
	if err := policy.NormalizeAndValidate(); err != nil {
		t.Fatal(err)
	}
	if policy.Groups[0].AutoBan.Enabled || policy.Groups[0].AutoBan.Scope != "SITE" {
		t.Fatalf("disabled auto-ban should default scope to SITE: %#v", policy.Groups[0].AutoBan)
	}
}

func TestWAFRuleSetVersionAcceptsKnownAndAutoUpdates(t *testing.T) {
	policy := DefaultWAFPolicy()
	policy.AutoUpdate = true
	policy.RuleSetVersion = ""
	if err := policy.NormalizeAndValidate(); err != nil {
		t.Fatal(err)
	}
	if policy.RuleSetVersion != LatestWAFRuleSetVersion {
		t.Fatalf("AutoUpdate did not pin latest: %q", policy.RuleSetVersion)
	}

	policy = DefaultWAFPolicy()
	policy.AutoUpdate = false
	policy.RuleSetVersion = KnownWAFRuleSetVersions[0]
	if err := policy.NormalizeAndValidate(); err != nil {
		t.Fatalf("known version rejected: %v", err)
	}

	policy = DefaultWAFPolicy()
	policy.AutoUpdate = false
	policy.RuleSetVersion = "2999.01.1"
	if err := policy.NormalizeAndValidate(); err == nil {
		t.Fatal("expected unknown version to be rejected")
	}
}
