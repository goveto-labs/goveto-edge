package policy

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDefaultDeliveryPolicyMarshalsEmptyCollectionsAsArrays(t *testing.T) {
	encoded, err := json.Marshal(DefaultDeliveryPolicy())
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"request_headers", "response_headers", "rewrites", "redirects", "allow_origins", "allow_headers", "expose_headers", "error_pages", "origin_pools", "splits"} {
		if !strings.Contains(string(encoded), `"`+field+`":[]`) {
			t.Fatalf("default delivery JSON does not contain %s as an array: %s", field, encoded)
		}
	}
}

func TestDeliveryPolicyNormalizesAndValidates(t *testing.T) {
	policy := DefaultDeliveryPolicy()
	policy.RequestHeaders = []HeaderRule{{Name: "x-origin-env", Value: "production"}}
	policy.Redirects = []RedirectRule{{Path: "/old/*", Location: "/new{http.request.uri.path}", Status: 308}}
	policy.CORS = CORSConfig{Enabled: true, AllowOrigins: []string{"https://app.example.com"}, AllowCredentials: true}
	policy.OriginPools = []PathOriginPool{{
		Name: "api", Paths: []string{"/api/*"}, Origins: []DeliveryOrigin{{Protocol: "HTTPS", Address: "api.internal:443"}},
	}}
	policy.Splits = []TrafficSplitRule{{Name: "canary", Pool: "api", CookieName: "cohort", Value: "canary"}}
	if err := policy.NormalizeAndValidate(); err != nil {
		t.Fatal(err)
	}
	if policy.RequestHeaders[0].Operation != "SET" || policy.OriginPools[0].Origins[0].Protocol != "https" {
		t.Fatalf("policy was not normalized: %#v", policy)
	}
}

func TestDeliveryPolicyRejectsUnsafeHeadersAndCORS(t *testing.T) {
	policy := DefaultDeliveryPolicy()
	policy.RequestHeaders = []HeaderRule{{Name: "Connection", Value: "close"}}
	if err := policy.NormalizeAndValidate(); err == nil {
		t.Fatal("hop-by-hop header was accepted")
	}
	policy = DefaultDeliveryPolicy()
	policy.CORS = CORSConfig{Enabled: true, AllowOrigins: []string{"*"}, AllowCredentials: true}
	if err := policy.NormalizeAndValidate(); err == nil {
		t.Fatal("credentialed wildcard CORS was accepted")
	}
}

func TestDeliveryPolicyRejectsInvalidCORSOrigins(t *testing.T) {
	for _, origin := range []string{
		"https://user@example.com",
		"https://example.com?tenant=one",
		"https://example.com#fragment",
	} {
		policy := DefaultDeliveryPolicy()
		policy.CORS = CORSConfig{Enabled: true, AllowOrigins: []string{origin}}
		if err := policy.NormalizeAndValidate(); err == nil {
			t.Fatalf("invalid CORS origin %q was accepted", origin)
		}
	}
}

func TestDeliveryPolicyValidatesPoolOriginsAndUniqueSplits(t *testing.T) {
	base := DefaultDeliveryPolicy()
	base.OriginPools = []PathOriginPool{{
		Name: "api", Paths: []string{"/api/*"}, Origins: []DeliveryOrigin{{Protocol: "https", Address: "api.internal:443"}},
	}}

	invalidAddress := base
	invalidAddress.OriginPools = append([]PathOriginPool(nil), base.OriginPools...)
	invalidAddress.OriginPools[0].Origins = []DeliveryOrigin{{Protocol: "https", Address: "https://api.internal"}}
	if err := invalidAddress.NormalizeAndValidate(); err == nil {
		t.Fatal("origin URL without a dial port was accepted")
	}

	invalidHost := base
	invalidHost.OriginPools = append([]PathOriginPool(nil), base.OriginPools...)
	invalidHost.OriginPools[0].Origins = []DeliveryOrigin{{Protocol: "https", Address: "api.internal:443", HostHeader: "bad\r\nhost"}}
	if err := invalidHost.NormalizeAndValidate(); err == nil {
		t.Fatal("invalid origin Host header was accepted")
	}

	duplicateSplits := base
	duplicateSplits.Splits = []TrafficSplitRule{
		{Name: "canary", Pool: "api", Percentage: 10},
		{Name: " canary ", Pool: "api", Percentage: 20},
	}
	if err := duplicateSplits.NormalizeAndValidate(); err == nil || !strings.Contains(err.Error(), "splits[1]") {
		t.Fatalf("duplicate split names were accepted: %v", err)
	}
}
