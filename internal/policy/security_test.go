package policy

import "testing"

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
