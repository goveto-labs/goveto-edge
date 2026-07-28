package waf

import (
	"net/http/httptest"
	"path/filepath"
	"testing"

	"goveto-edge/internal/policy"
)

func TestAccessPolicyMatchesCountryAndSubdivisionFromCityDatabase(t *testing.T) {
	fixture := filepath.Join("..", "..", "internal", "testdata", "GeoIP2-City-Test.mmdb")
	tests := []struct {
		name      string
		countries []string
		regions   []string
		wantRule  string
	}{
		{name: "allowed country and subdivision", countries: []string{"GB"}, regions: []string{"GB-ENG"}},
		{name: "country not allowed", countries: []string{"US"}, wantRule: "access:country-allowlist"},
		{name: "subdivision not allowed", countries: []string{"GB"}, regions: []string{"US-NY"}, wantRule: "access:region-allowlist"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			access := policy.DefaultAccessPolicy()
			access.GeoIPDatabase = fixture
			access.AllowedCountries = test.countries
			access.AllowedRegions = test.regions
			compiled, err := compileAccess(access)
			if err != nil {
				t.Fatal(err)
			}
			defer compiled.geo.Close()
			request := httptest.NewRequest("GET", "http://example.test/", nil)
			request.RemoteAddr = "81.2.69.160:1234"
			decision := compiled.match(request, "81.2.69.160")
			if test.wantRule == "" {
				if decision != nil {
					t.Fatalf("unexpected decision: %#v", decision)
				}
			} else if decision == nil || decision.ruleID != test.wantRule {
				t.Fatalf("decision=%#v want rule %q", decision, test.wantRule)
			}
		})
	}
}

func TestCompileAccessRejectsGeoRestrictionsWithoutDatabase(t *testing.T) {
	access := policy.DefaultAccessPolicy()
	access.BlockedCountries = []string{"GB"}
	if _, err := compileAccess(access); err == nil {
		t.Fatal("GeoIP restriction without a database was accepted")
	}
}
