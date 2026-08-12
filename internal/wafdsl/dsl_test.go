package wafdsl

import (
	"reflect"
	"strings"
	"testing"

	"goveto-edge/internal/policy"
)

func TestPolicyRoundTrip(t *testing.T) {
	original := policy.DefaultWAFPolicy()
	if err := original.NormalizeAndValidatePublic(); err != nil {
		t.Fatal(err)
	}
	source, err := Render(original, ScopePolicy, Target{})
	if err != nil {
		t.Fatal(err)
	}
	result := ParseAndApply(original, ScopePolicy, Target{}, source)
	if !result.Valid {
		t.Fatalf("diagnostics: %#v\n%s", result.Diagnostics, source)
	}
	if result.Policy == nil || !reflect.DeepEqual(original, *result.Policy) {
		t.Fatalf("round trip changed policy\noriginal=%#v\nactual=%#v", original, result.Policy)
	}
}

func TestCloudflareStyleRule(t *testing.T) {
	source := `rule "admin" "Admin protection" enabled {
  when (
    (http.request.uri.path starts_with "/admin" and http.request.method in {"POST", "PUT"})
    or
    (ip.src in {192.0.2.0/24} and not http.request.headers["X-Allow"] exists)
  )
  then block status 403
}`
	parsed, diagnostics := Parse(source, ScopeRule)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics: %#v", diagnostics)
	}
	rule := parsed.(policy.WAFRule)
	if rule.Conditions.Operator != "OR" || len(rule.Conditions.Groups) != 2 || rule.Conditions.Groups[0].Operator != "AND" {
		t.Fatalf("unexpected conditions: %#v", rule.Conditions)
	}
	if rule.Conditions.Groups[1].Conditions[0].Operator != "CIDR" || !rule.Conditions.Groups[1].Conditions[1].Negate {
		t.Fatalf("unexpected conditions: %#v", rule.Conditions.Groups[1].Conditions)
	}
}

func TestClientIPAddressSetUsesHostCIDRs(t *testing.T) {
	source := `rule "addresses" "Address match" enabled {
  when ((ip.src in {192.0.2.1, 198.51.100.0/24, 2001:db8::1}))
  then block status 403
}`
	parsed, diagnostics := Parse(source, ScopeRule)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics: %#v", diagnostics)
	}
	condition := parsed.(policy.WAFRule).Conditions.Groups[0].Conditions[0]
	if condition.Operator != "CIDR" {
		t.Fatalf("operator = %q, want CIDR", condition.Operator)
	}
	want := []string{"192.0.2.1/32", "198.51.100.0/24", "2001:db8::1/128"}
	if !reflect.DeepEqual(condition.Values, want) {
		t.Fatalf("values = %#v, want %#v", condition.Values, want)
	}
}

func TestApplyGroupPreservesTargetAndSiblingIDs(t *testing.T) {
	current := policy.DefaultWAFPolicy()
	rule := &current.RuleSets[0].Rules[0]
	rule.Conditions.Groups[0].ID = "group-id"
	rule.Conditions.Groups[0].Conditions[0].ID = "condition-id"
	target := Target{RuleSetID: current.RuleSets[0].ID, RuleID: rule.ID, GroupID: "group-id"}
	result := ParseAndApply(current, ScopeGroup, target, `group (http.request.uri.path starts_with "/safe" and http.request.method eq "GET")`)
	if !result.Valid {
		t.Fatalf("diagnostics: %#v", result.Diagnostics)
	}
	group := result.Policy.RuleSets[0].Rules[0].Conditions.Groups[0]
	if group.ID != "group-id" || group.Conditions[0].ID != "condition-id" || group.Conditions[1].ID == "" {
		t.Fatalf("IDs were not reconciled: %#v", group)
	}
	if result.Policy.RuleSets[1].ID != current.RuleSets[1].ID {
		t.Fatal("unrelated rule set changed")
	}
}

func TestDiagnosticsRejectMixedGroupOperators(t *testing.T) {
	_, diagnostics := Parse(`group (http.request.method eq "GET" and http.host eq "a" or http.host eq "b")`, ScopeGroup)
	if len(diagnostics) != 1 || !strings.Contains(diagnostics[0].Message, "one consistent") || diagnostics[0].Line < 1 {
		t.Fatalf("unexpected diagnostics: %#v", diagnostics)
	}
}

func TestRateLimitRoundTrip(t *testing.T) {
	source := `rate_limit "login" "Login" enabled {
  when ((http.request.uri.path eq "/login"))
  count_by ip.src + http.request.uri.path
  limit 60 per 60s burst 20
  backend redis fallback local
  then show_page status 429 response default
}`
	parsed, diagnostics := Parse(source, ScopeRule)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics: %#v", diagnostics)
	}
	rule := parsed.(policy.WAFRule)
	if rule.Type != policy.WAFRuleTypeRateLimit || rule.Key != "CLIENT_IP_PATH" || rule.Backend != "REDIS" {
		t.Fatalf("unexpected rule: %#v", rule)
	}
}

func TestNamedFieldsAndActions(t *testing.T) {
	tests := []struct {
		name   string
		field  string
		action string
	}{
		{"header tag", `http.request.headers["X-Risk"] contains "high"`, `tag "risk.high"`},
		{"cookie redirect", `http.request.cookies["session"] exists`, `redirect "/login" status 302`},
		{"query allow", `http.request.uri.query["token"] eq "safe" case_sensitive`, `allow`},
		{"body monitor", `http.request.body.raw matches "attack"`, `monitor`},
		{"country captcha", `ip.src.country in {"US", "GB"}`, `captcha`},
		{"html page", `http.host eq "blocked.example"`, `show_page status 451 response html "<h1>Blocked</h1>"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := `rule "test" "Test" enabled { when ((` + test.field + `)) then ` + test.action + ` }`
			parsed, diagnostics := Parse(source, ScopeRule)
			if len(diagnostics) != 0 {
				t.Fatalf("diagnostics: %#v", diagnostics)
			}
			rule := parsed.(policy.WAFRule)
			if rule.Conditions.Groups[0].Conditions[0].Field == "" || rule.Action.Type == "" {
				t.Fatalf("unexpected rule: %#v", rule)
			}
		})
	}
}

func TestLexerDiagnosticPosition(t *testing.T) {
	_, diagnostics := Parse("group (\n  http.host eq @\n)", ScopeGroup)
	if len(diagnostics) != 1 || diagnostics[0].Line != 2 || diagnostics[0].Column < 10 {
		t.Fatalf("unexpected diagnostics: %#v", diagnostics)
	}
}
