package securitystate

import (
	"strings"
	"testing"
)

func TestSecurityStateKeySpacesAreStableAndSeparated(t *testing.T) {
	keys := []string{
		RateCounterKey("site", "rule", "value"),
		ChallengeKey("token"),
	}
	seen := map[string]bool{}
	for _, key := range keys {
		if seen[key] || strings.Contains(key, "value") || strings.Contains(key, "token") {
			t.Fatalf("key is duplicated or leaks source material: %q", key)
		}
		seen[key] = true
	}
}
