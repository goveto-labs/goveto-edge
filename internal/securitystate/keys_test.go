package securitystate

import (
	"net/netip"
	"strings"
	"testing"
)

func TestSecurityStateKeySpacesAreStableAndSeparated(t *testing.T) {
	address := netip.MustParseAddr("192.0.2.10")
	keys := []string{
		RateCounterKey("site", "rule", "value"),
		RateBlockKey("site", "rule", "value"),
		ChallengeKey("token"),
		GlobalBlockKey(address),
		SiteBlockKey("site", address),
		WAFAutoBanCounterKey("site", "group", address),
	}
	seen := map[string]bool{}
	for _, key := range keys {
		if seen[key] || strings.Contains(key, "value") || strings.Contains(key, "token") || strings.Contains(key, address.String()) {
			t.Fatalf("key is duplicated or leaks source material: %q", key)
		}
		seen[key] = true
	}
}
