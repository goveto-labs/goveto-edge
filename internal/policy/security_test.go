package policy

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDefaultSecurityPoliciesSerializeEmptyCollectionsAsArrays(t *testing.T) {
	value := struct {
		WAF       WAFPolicy       `json:"waf"`
		RateLimit RateLimitPolicy `json:"rate_limit"`
	}{WAF: DefaultWAFPolicy(), RateLimit: DefaultRateLimitPolicy()}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, expected := range []string{`"groups":[]`, `"rules":[]`} {
		if !strings.Contains(text, expected) {
			t.Fatalf("default security JSON missing %s: %s", expected, text)
		}
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
