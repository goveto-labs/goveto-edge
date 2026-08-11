package waf

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/caddyserver/caddy/v2"

	"goveto-edge/internal/policy"
)

func TestWAFRuleMatchesCountryAndRegion(t *testing.T) {
	waf := singleRulePolicy(policy.WAFRule{
		ID: "geo-block", Enabled: true, Type: policy.WAFRuleTypeMatch,
		Conditions: policy.WAFConditions{Operator: "AND", Groups: []policy.WAFConditionGroup{{
			Operator: "AND", Conditions: []policy.WAFCondition{
				{Field: "COUNTRY", Operator: "EQUALS", Value: "GB"},
				{Field: "REGION", Operator: "EQUALS", Value: "GB-ENG"},
			},
		}}},
		Action: policy.WAFAction{Type: policy.WAFActionBlock, StatusCode: http.StatusForbidden},
	})
	waf.GeoIPDatabase = filepath.Join("..", "..", "internal", "testdata", "GeoIP2-City-Test.mmdb")
	h := provisionHandler(t, "geo-rule", waf)
	request := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	request.RemoteAddr = "81.2.69.160:1234"
	response := httptest.NewRecorder()
	if err := h.ServeHTTP(response, request, &nextHandler{}); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusForbidden || response.Header().Get("X-Goveto-WAF-Rule") != "geo-block" {
		t.Fatalf("status=%d headers=%v", response.Code, response.Header())
	}
}

func TestWAFGeoRuleRequiresManagedDatabase(t *testing.T) {
	waf := singleRulePolicy(policy.WAFRule{
		ID: "geo", Enabled: true, Type: policy.WAFRuleTypeMatch,
		Conditions: testConditions(policy.WAFCondition{Field: "COUNTRY", Operator: "EQUALS", Value: "GB"}),
		Action:     policy.WAFAction{Type: policy.WAFActionBlock, StatusCode: http.StatusForbidden},
	})
	h := &Handler{SiteID: "missing-geo", WAF: waf}
	if err := h.Provision(caddy.Context{}); err == nil {
		t.Fatal("GeoIP rule without a managed database was accepted")
	}
}

func TestParseAddressUnmapsIPv4MappedAddress(t *testing.T) {
	address, err := parseAddress("::ffff:192.168.4.23")
	if err != nil {
		t.Fatal(err)
	}
	if got := address.String(); got != "192.168.4.23" {
		t.Fatalf("parsed mapped IPv4 = %q", got)
	}
}
