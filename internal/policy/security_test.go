package policy

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestDefaultWAFPolicyContainsOrderedBuiltins(t *testing.T) {
	policy := DefaultWAFPolicy()
	want := []string{"builtin-xss", "builtin-path-traversal", "builtin-sensitive-directories", "builtin-sql-injection", "builtin-cc"}
	if !policy.Enabled || len(policy.RuleSets) != len(want) {
		t.Fatalf("unexpected defaults: %#v", policy)
	}
	for index, id := range want {
		if policy.RuleSets[index].ID != id || !policy.RuleSets[index].Enabled {
			t.Fatalf("rule set %d = %#v, want %q enabled", index, policy.RuleSets[index], id)
		}
	}
	cc := policy.RuleSets[4].Rules[0]
	if cc.Type != WAFRuleTypeRateLimit || cc.Key != "CLIENT_IP_PATH" || cc.Requests != 60 || cc.WindowSeconds != 60 || cc.Burst != 20 {
		t.Fatalf("unexpected CC defaults: %#v", cc)
	}
	if cc.Backend != "REDIS" || cc.FailureMode != "LOCAL" {
		t.Fatalf("unexpected CC backend semantics: %#v", cc)
	}
	if cc.Action.Type != WAFActionShowPage || cc.Action.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("unexpected CC action: %#v", cc.Action)
	}
}

func TestWAFPolicyMissingRuleSetsGetsFreshBuiltins(t *testing.T) {
	var policy WAFPolicy
	if err := json.Unmarshal([]byte(`{"enabled":false,"engine":"CORAZA_CRS","groups":[{}]}`), &policy); err != nil {
		t.Fatal(err)
	}
	if err := policy.NormalizeAndValidate(); err != nil {
		t.Fatal(err)
	}
	if policy.Enabled || len(policy.RuleSets) != 5 {
		t.Fatalf("legacy fields affected normalized policy: %#v", policy)
	}
}

func TestWAFPolicyPreservesRuleSetAndRuleOrder(t *testing.T) {
	policy := WAFPolicy{Enabled: true, RuleSets: []WAFRuleSet{
		{ID: "second", Enabled: true, Rules: []WAFRule{matchTestRule("b"), matchTestRule("a")}},
		{ID: "first", Enabled: true, Rules: []WAFRule{matchTestRule("c")}},
	}}
	if err := policy.NormalizeAndValidate(); err != nil {
		t.Fatal(err)
	}
	if policy.RuleSets[0].ID != "second" || policy.RuleSets[0].Rules[0].ID != "b" || policy.RuleSets[0].Rules[1].ID != "a" {
		t.Fatalf("normalization changed ordering: %#v", policy.RuleSets)
	}
}

func TestWAFPolicyTrustedProxyChainValidation(t *testing.T) {
	policy := DefaultWAFPolicy()
	if policy.TrustedProxyChain || len(policy.TrustedProxies) != 0 {
		t.Fatalf("trusted proxy chain must default off: %#v", policy)
	}
	policy.TrustedProxyChain = true
	if err := policy.NormalizeAndValidate(); err == nil {
		t.Fatal("trusted proxy chain without trusted proxies was accepted")
	}
	policy.TrustedProxies = []string{" 10.0.0.9 ", "10.0.0.0/8", "::ffff:192.0.2.4", "::ffff:198.51.100.0/120", "10.0.0.0/8"}
	if err := policy.NormalizeAndValidate(); err != nil {
		t.Fatal(err)
	}
	want := []string{"10.0.0.9/32", "10.0.0.0/8", "192.0.2.4/32", "198.51.100.0/24"}
	if len(policy.TrustedProxies) != len(want) {
		t.Fatalf("trusted proxies = %#v, want %#v", policy.TrustedProxies, want)
	}
	for index := range want {
		if policy.TrustedProxies[index] != want[index] {
			t.Fatalf("trusted proxy %d = %q, want %q", index, policy.TrustedProxies[index], want[index])
		}
	}
	policy.TrustedProxies = []string{"not-an-address"}
	if err := policy.NormalizeAndValidate(); err == nil {
		t.Fatal("invalid trusted proxy was accepted")
	}
}

func TestWAFPolicyValidatesRulesAndActions(t *testing.T) {
	tests := []WAFRule{
		{ID: "regex", Enabled: true, Type: WAFRuleTypeMatch, Conditions: singleWAFCondition(WAFCondition{Field: "PATH", Operator: "REGEX", Value: "["}), Action: WAFAction{Type: WAFActionBlock}},
		{ID: "rate", Enabled: true, Type: WAFRuleTypeRateLimit, Key: "CLIENT_IP", Requests: 0, WindowSeconds: 60, Action: WAFAction{Type: WAFActionShowPage}},
		{ID: "backend", Enabled: true, Type: WAFRuleTypeRateLimit, Key: "CLIENT_IP", Requests: 1, WindowSeconds: 60, Backend: "SQL", Action: WAFAction{Type: WAFActionShowPage}},
		{ID: "failure", Enabled: true, Type: WAFRuleTypeRateLimit, Key: "CLIENT_IP", Requests: 1, WindowSeconds: 60, Backend: "REDIS", FailureMode: "IGNORE", Action: WAFAction{Type: WAFActionShowPage}},
		{ID: "redirect", Enabled: true, Type: WAFRuleTypeMatch, Conditions: singleWAFCondition(WAFCondition{Field: "PATH", Operator: "EQUALS", Value: "/"}), Action: WAFAction{Type: WAFActionRedirect, RedirectURL: "javascript:alert(1)"}},
		{ID: "tag", Enabled: true, Type: WAFRuleTypeMatch, Conditions: singleWAFCondition(WAFCondition{Field: "PATH", Operator: "EQUALS", Value: "/"}), Action: WAFAction{Type: WAFActionTag, Tag: "bad tag"}},
	}
	for _, rule := range tests {
		policy := WAFPolicy{Enabled: true, RuleSets: []WAFRuleSet{{ID: "set", Enabled: true, Rules: []WAFRule{rule}}}}
		if err := policy.NormalizeAndValidate(); err == nil {
			t.Fatalf("rule %q unexpectedly validated", rule.ID)
		}
	}
}

func matchTestRule(id string) WAFRule {
	return WAFRule{ID: id, Enabled: true, Type: WAFRuleTypeMatch, Conditions: singleWAFCondition(WAFCondition{Field: "PATH", Operator: "EQUALS", Value: "/"}), Action: WAFAction{Type: WAFActionMonitor}}
}

func TestWAFPolicyNormalizesCompoundConditions(t *testing.T) {
	policy := WAFPolicy{Enabled: true, RuleSets: []WAFRuleSet{{ID: "compound", Enabled: true, Rules: []WAFRule{{
		ID: "rule", Enabled: true, Type: WAFRuleTypeMatch,
		Conditions: WAFConditions{Operator: "and", Groups: []WAFConditionGroup{
			{Operator: "and", Conditions: []WAFCondition{{Field: "method", Operator: "equals", Value: "POST"}}},
			{Operator: "or", Conditions: []WAFCondition{{Field: "path", Operator: "equals", Value: "/login"}, {Field: "path", Operator: "equals", Value: "/register"}}},
		}},
		Action: WAFAction{Type: WAFActionBlock},
	}}}}}
	if err := policy.NormalizeAndValidate(); err != nil {
		t.Fatal(err)
	}
	conditions := policy.RuleSets[0].Rules[0].Conditions
	if conditions.Operator != "AND" || conditions.Groups[1].Operator != "OR" || conditions.Groups[1].Conditions[0].Field != "PATH" {
		t.Fatalf("conditions were not normalized: %#v", conditions)
	}
}

func TestWAFPolicySupportsGeoConditionsAndRejectsPublicDatabasePath(t *testing.T) {
	policy := WAFPolicy{Enabled: true, GeoIPDatabase: "/client/controlled.mmdb", RuleSets: []WAFRuleSet{{
		ID: "geo", Enabled: true, Rules: []WAFRule{{
			ID: "country", Enabled: true, Type: WAFRuleTypeMatch,
			Conditions: singleWAFCondition(WAFCondition{Field: "country", Operator: "IN", Values: []string{"GB", "US"}}),
			Action:     WAFAction{Type: WAFActionBlock},
		}},
	}}}
	if err := policy.NormalizeAndValidatePublic(); err != nil {
		t.Fatal(err)
	}
	if policy.GeoIPDatabase != "" || !policy.NeedsGeoIP() || policy.RuleSets[0].Rules[0].Conditions.Groups[0].Conditions[0].Field != "COUNTRY" {
		t.Fatalf("unexpected normalized GeoIP policy: %#v", policy)
	}
}
