package policy

import (
	"net/http"
	"testing"
)

func cachePolicyWithCatchAllRule() CachePolicy {
	policy := DefaultCachePolicy()
	policy.Rules = []CacheRule{{
		Name: "Default",
		TTL: CacheTTL{
			DefaultSeconds: 300,
			Status:         map[string]int{"200": 300, "301": 3600, "404": 60},
			ClientSeconds:  300,
		},
		Conditions: CacheConditions{
			GroupOperator: "OR",
			Groups: []CacheConditionGroup{{
				Operator: "OR",
				Rules:    []CacheConditionRule{{Type: "ALL"}},
			}},
		},
	}}
	return policy
}

func TestDefaultCachePolicyValid(t *testing.T) {
	policy := DefaultCachePolicy()
	if err := policy.NormalizeAndValidate(); err != nil {
		t.Fatal(err)
	}
	if len(policy.Rules) != 0 {
		t.Fatalf("default cache policy must not match requests: %#v", policy.Rules)
	}
	if !policy.RequestCoalescing || !policy.CacheRangeRequests || policy.MaxBodyBytes != 64<<20 {
		t.Fatalf("unsafe cache defaults: %#v", policy)
	}
	if policy.Stale.WhileRevalidateSeconds != 30 {
		t.Fatalf("stale-while-revalidate default = %d", policy.Stale.WhileRevalidateSeconds)
	}
	if len(policy.CacheKey.Headers) != 0 {
		t.Fatalf("managed representation headers leaked into the default cache key: %#v", policy.CacheKey.Headers)
	}
	if got := policy.CacheKey.Parts; len(got) != 4 || got[0] != CacheKeyPartMethod || got[1] != CacheKeyPartHost || got[2] != CacheKeyPartPath || got[3] != CacheKeyPartQuery {
		t.Fatalf("unexpected default cache key parts: %#v", got)
	}
	if got := policy.Methods; len(got) != 2 || got[0] != http.MethodGet || got[1] != http.MethodHead {
		t.Fatalf("unexpected default cache methods: %#v", got)
	}
}

func TestCachePolicyRemovesManagedAcceptEncodingHeader(t *testing.T) {
	policy := DefaultCachePolicy()
	policy.CacheKey.Headers = []string{"X-Variant", "accept-encoding", "Accept-Language"}
	if err := policy.NormalizeAndValidate(); err != nil {
		t.Fatal(err)
	}
	if got := policy.CacheKey.Headers; len(got) != 2 || got[0] != "Accept-Language" || got[1] != "X-Variant" {
		t.Fatalf("managed cache key header was not removed: %#v", got)
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
	policy := cachePolicyWithCatchAllRule()
	policy.Rules[0].Conditions.Groups[0].Rules = []CacheConditionRule{{Type: "ALL"}, {Type: "PATH_REGEX", Value: "["}}
	if err := policy.NormalizeAndValidate(); err == nil {
		t.Fatal("expected invalid conditions")
	}
}

func TestCachePolicyNormalizesAdvancedSettings(t *testing.T) {
	policy := cachePolicyWithCatchAllRule()
	policy.Methods = []string{"post", "get"}
	policy.CacheKey.Parts = []string{"query", "path", "host"}
	policy.CacheKey.Headers = []string{"accept-language", "x-variant"}
	policy.BypassCacheControl = []string{"MAX-AGE=0", "No-Store"}
	policy.Rules[0].TTL.OverrideClientTTL = true
	policy.Rules[0].TTL.ClientSeconds = 0
	if err := policy.NormalizeAndValidate(); err != nil {
		t.Fatal(err)
	}
	if policy.Methods[0] != "GET" || policy.Methods[1] != "POST" {
		t.Fatalf("methods were not normalized: %#v", policy.Methods)
	}
	if policy.CacheKey.Headers[0] != "Accept-Language" || policy.BypassCacheControl[0] != "max-age=0" {
		t.Fatalf("advanced settings were not normalized: %#v", policy)
	}
	if got := policy.CacheKey.Parts; len(got) != 3 || got[0] != CacheKeyPartHost || got[1] != CacheKeyPartPath || got[2] != CacheKeyPartQuery {
		t.Fatalf("cache key parts were not normalized: %#v", got)
	}
}

func TestCachePolicyRejectsInvalidAdvancedSettings(t *testing.T) {
	for _, mutate := range []func(*CachePolicy){
		func(policy *CachePolicy) { policy.Methods = nil },
		func(policy *CachePolicy) { policy.Methods = []string{"TRACE"} },
		func(policy *CachePolicy) { policy.CacheKey.Parts = []string{CacheKeyPartHost} },
		func(policy *CachePolicy) { policy.CacheKey.Parts = []string{CacheKeyPartPath, "COOKIE"} },
		func(policy *CachePolicy) { policy.CacheKey.Parts = []string{CacheKeyPartPath, CacheKeyPartPath} },
		func(policy *CachePolicy) { policy.CacheKey.Headers = []string{"X Invalid"} },
		func(policy *CachePolicy) { policy.CacheKey.Headers = []string{"X-Test", "x-test"} },
		func(policy *CachePolicy) { policy.BypassCacheControl = []string{"max-age = nope value"} },
		func(policy *CachePolicy) {
			policy.Rules[0].TTL.OverrideClientTTL = true
			policy.Rules[0].TTL.ClientSeconds = -1
		},
	} {
		policy := cachePolicyWithCatchAllRule()
		mutate(&policy)
		if err := policy.NormalizeAndValidate(); err == nil {
			t.Fatalf("expected advanced cache validation error: %#v", policy)
		}
	}
}

func TestCachePolicyValidatesOrderedPerRuleTTL(t *testing.T) {
	policy := cachePolicyWithCatchAllRule()
	fallback := policy.Rules[0]
	asset := CacheRule{
		Name: "Assets",
		TTL:  CacheTTL{DefaultSeconds: 3600, Status: map[string]int{"404": 30}, ClientSeconds: 300},
		Conditions: CacheConditions{GroupOperator: "OR", Groups: []CacheConditionGroup{{
			Operator: "OR", Rules: []CacheConditionRule{{Type: "PATH_PREFIX", Values: []string{"/assets/"}}},
		}}},
	}
	policy.Rules = []CacheRule{asset, fallback}
	if err := policy.NormalizeAndValidate(); err != nil {
		t.Fatal(err)
	}
	policy.Rules[0], policy.Rules[1] = policy.Rules[1], policy.Rules[0]
	if err := policy.NormalizeAndValidate(); err == nil {
		t.Fatal("expected catch-all cache rule ordering error")
	}
	policy = cachePolicyWithCatchAllRule()
	policy.Rules = append(policy.Rules, policy.Rules[0])
	if err := policy.NormalizeAndValidate(); err == nil {
		t.Fatal("expected duplicate cache rule name")
	}
}

func TestCacheKeyQueryNormalizationDefaultsOn(t *testing.T) {
	policy := DefaultCachePolicy()
	if !policy.CacheKey.Query.NormalizeEnabled() {
		t.Fatal("default cache key query normalization must be enabled")
	}
	// Missing JSON field (nil pointer) becomes explicit true on validate so
	// existing site policies opt into sorting unless they set false.
	legacy := cachePolicyWithCatchAllRule()
	legacy.CacheKey.Query = QueryKey{}
	if err := legacy.NormalizeAndValidate(); err != nil {
		t.Fatal(err)
	}
	if legacy.CacheKey.Query.Normalize == nil || !*legacy.CacheKey.Query.Normalize {
		t.Fatal("NormalizeAndValidate must materialize normalize=true for omitted values")
	}
	disabled := cachePolicyWithCatchAllRule()
	disabled.CacheKey.Query.Normalize = boolPtr(false)
	if err := disabled.NormalizeAndValidate(); err != nil {
		t.Fatal(err)
	}
	if disabled.CacheKey.Query.NormalizeEnabled() {
		t.Fatal("explicit normalize=false must be preserved")
	}
}

func TestCacheKeyQueryIncludeAndExcludeAreMutuallyExclusive(t *testing.T) {
	policy := cachePolicyWithCatchAllRule()
	policy.CacheKey.Query.Include = []string{"id"}
	policy.CacheKey.Query.Exclude = []string{"utm_source"}
	if err := policy.NormalizeAndValidate(); err == nil {
		t.Fatal("expected include/exclude mutual exclusion error")
	}
}

func TestCacheKeyQueryListsAreLowercasedAndSorted(t *testing.T) {
	policy := cachePolicyWithCatchAllRule()
	policy.CacheKey.Query.Exclude = []string{"UTM_Source", "b", "a"}
	if err := policy.NormalizeAndValidate(); err != nil {
		t.Fatal(err)
	}
	if got := policy.CacheKey.Query.Exclude; len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "utm_source" {
		t.Fatalf("exclude list not normalized: %#v", got)
	}
}

func TestCacheKeyQueryRejectsBadNamesAndDuplicates(t *testing.T) {
	for _, mutate := range []func(*CachePolicy){
		func(p *CachePolicy) { p.CacheKey.Query.Exclude = []string{"bad name"} },
		func(p *CachePolicy) { p.CacheKey.Query.Include = []string{"x", "x"} },
		func(p *CachePolicy) { p.CacheKey.Query.Include = []string{"\""} },
	} {
		policy := cachePolicyWithCatchAllRule()
		mutate(&policy)
		if err := policy.NormalizeAndValidate(); err == nil {
			t.Fatalf("expected query list validation error: %#v", policy.CacheKey.Query)
		}
	}
}

func TestCacheKeyCookiesAreSortedAndValidated(t *testing.T) {
	policy := cachePolicyWithCatchAllRule()
	policy.CacheKey.Cookies = []string{"session", "ab_test", "session"}
	if err := policy.NormalizeAndValidate(); err == nil {
		t.Fatal("expected duplicate cookie error")
	}
	policy.CacheKey.Cookies = []string{"session", "ab_test"}
	if err := policy.NormalizeAndValidate(); err != nil {
		t.Fatal(err)
	}
	if got := policy.CacheKey.Cookies; got[0] != "ab_test" || got[1] != "session" {
		t.Fatalf("cookies not sorted: %#v", got)
	}
	policy.CacheKey.Cookies = []string{"bad cookie"}
	if err := policy.NormalizeAndValidate(); err == nil {
		t.Fatal("expected invalid cookie name error")
	}
}
