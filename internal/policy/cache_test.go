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
	if !policy.RequestCoalescing || !policy.CacheRangeRequests || policy.MaxBodyBytes != 64<<20 {
		t.Fatalf("unsafe cache defaults: %#v", policy)
	}
	if policy.Stale.WhileRevalidateSeconds != 30 {
		t.Fatalf("stale-while-revalidate default = %d", policy.Stale.WhileRevalidateSeconds)
	}
}

func TestCachePolicyRejectsInvalidStaleAndBodyLimits(t *testing.T) {
	policy := DefaultCachePolicy()
	policy.Stale.WhileRevalidateSeconds = -1
	if err := policy.NormalizeAndValidate(); err == nil {
		t.Fatal("expected invalid stale-while-revalidate")
	}
	policy = DefaultCachePolicy()
	policy.MaxBodyBytes = 4<<30 + 1
	if err := policy.NormalizeAndValidate(); err == nil {
		t.Fatal("expected invalid body limit")
	}
}

func TestCachePolicyRejectsInvalidRegexAndMixedAll(t *testing.T) {
	policy := DefaultCachePolicy()
	policy.Conditions.Groups[0].Rules = []CacheConditionRule{{Type: "ALL"}, {Type: "PATH_REGEX", Value: "["}}
	if err := policy.NormalizeAndValidate(); err == nil {
		t.Fatal("expected invalid conditions")
	}
}
