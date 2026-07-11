package policy

import "testing"

func TestDefaultCachePolicyValid(t *testing.T) {
	policy := DefaultCachePolicy()
	if err := policy.NormalizeAndValidate(); err != nil {
		t.Fatal(err)
	}
	if policy.Enabled {
		t.Fatal("cache must be disabled by default")
	}
}

func TestCachePolicyRejectsInvalidRegexAndMixedAll(t *testing.T) {
	policy := DefaultCachePolicy()
	policy.Conditions.Groups[0].Rules = []CacheConditionRule{{Type: "ALL"}, {Type: "PATH_REGEX", Value: "["}}
	if err := policy.NormalizeAndValidate(); err == nil {
		t.Fatal("expected invalid conditions")
	}
}
