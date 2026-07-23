package policy

import "testing"

func TestDefaultCompressionPolicyValid(t *testing.T) {
	policy := DefaultCompressionPolicy()
	if err := policy.NormalizeAndValidate(); err != nil {
		t.Fatal(err)
	}
	if policy.Enabled {
		t.Fatal("compression must be disabled by default")
	}
}

func TestCompressionPolicyNormalizesValues(t *testing.T) {
	policy := DefaultCompressionPolicy()
	policy.Extensions = []string{".HTML", "css"}
	policy.MIMETypes = []string{"Text/*", "application/json"}
	policy.ExcludedPaths = []string{"/downloads", "/api/export"}
	if err := policy.NormalizeAndValidate(); err != nil {
		t.Fatal(err)
	}
	if policy.Extensions[0] != "css" || policy.Extensions[1] != "html" {
		t.Fatalf("extensions were not normalized: %#v", policy.Extensions)
	}
	if policy.MIMETypes[1] != "text/*" {
		t.Fatalf("MIME types were not normalized: %#v", policy.MIMETypes)
	}
}

func TestCompressionPolicyRejectsInvalidLimitsAndMatchers(t *testing.T) {
	tests := []CompressionPolicy{
		{Enabled: true, MinimumLength: 100, MaximumLength: 10, Extensions: []string{"html"}},
		{Enabled: true, MaximumLength: 100, Extensions: []string{"bad value"}},
		{Enabled: true, MaximumLength: 100, MIMETypes: []string{"invalid"}},
		{Enabled: true, MaximumLength: 100, ExcludedPaths: []string{"api"}, Extensions: []string{"html"}},
		{Enabled: true, MaximumLength: 100},
	}
	for index := range tests {
		if err := tests[index].NormalizeAndValidate(); err == nil {
			t.Fatalf("case %d: expected validation error", index)
		}
	}
}
