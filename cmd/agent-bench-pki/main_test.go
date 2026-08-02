package main

import (
	"testing"

	cachepolicy "goveto-edge/internal/policy"
)

func TestBenchmarkCachePolicies(t *testing.T) {
	standard, complex, err := benchmarkCachePolicies()
	if err != nil {
		t.Fatal(err)
	}
	if len(standard.Rules) != 1 || standard.Rules[0].Conditions.Groups[0].Rules[0].Type != "ALL" {
		t.Fatalf("standard cache policy must contain one catch-all rule: %#v", standard.Rules)
	}
	if err := standard.NormalizeAndValidate(); err != nil {
		t.Fatalf("standard cache policy is invalid: %v", err)
	}

	if len(complex.Rules) != 32 {
		t.Fatalf("complex cache policy has %d rules, want the maximum 32", len(complex.Rules))
	}
	if complex.Rules[0].Conditions.Groups[0].Rules[0].Type != "EXTENSION" {
		t.Fatalf("first complex rule does not exercise extension matching: %#v", complex.Rules[0])
	}
	if complex.Rules[30].Conditions.Groups[1].Rules[0].Type != "PATH_REGEX" {
		t.Fatalf("late complex rule does not exercise grouped regex matching: %#v", complex.Rules[30])
	}
	if complex.Rules[31].Conditions.Groups[0].Rules[0].Type != "ALL" {
		t.Fatalf("complex cache fallback is not last: %#v", complex.Rules[31])
	}
	if !complex.CacheKey.Hash || !complex.CacheKey.Hide || len(complex.CacheKey.Headers) != 2 ||
		len(complex.CacheKey.Parts) != 5 || complex.CacheKey.Parts[1] != cachepolicy.CacheKeyPartScheme {
		t.Fatalf("complex cache key does not exercise advanced settings: %#v", complex.CacheKey)
	}
	if err := complex.NormalizeAndValidate(); err != nil {
		t.Fatalf("complex cache policy is invalid: %v", err)
	}
}
