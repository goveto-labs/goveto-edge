package main

import "testing"

func TestBaselineRunID(t *testing.T) {
	path := "/results/current/baseline/20260803T025637Z/cache/cache-hot-1024b-h1-c32/report.json"
	if got := baselineRunID(path); got != "20260803T025637Z" {
		t.Fatalf("baselineRunID(%q) = %q", path, got)
	}
}
