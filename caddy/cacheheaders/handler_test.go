package cacheheaders

import "testing"

func TestCacheResult(t *testing.T) {
	for input, want := range map[string]string{
		"Goveto; hit; ttl=10":          "HIT",
		"Goveto; fwd=uri-miss; stored": "MISS",
		"Goveto; fwd=stale":            "STALE",
		"":                             "BYPASS",
	} {
		if got := cacheResult(input); got != want {
			t.Fatalf("cacheResult(%q)=%q want %q", input, got, want)
		}
	}
}
