package edgecontrol

import (
	"strings"
	"testing"
)

func TestSiteTombstoneDeduplicatesCompletedVersion(t *testing.T) {
	for _, fragment := range []string{
		"t.status IN ('PENDING','RUNNING','SUCCEEDED')",
		"t.payload->>'site_id'=$6",
		"(t.payload->>'disabled')::boolean",
		"(t.payload->>'version')::numeric,0) >= $7::numeric",
		"ON CONFLICT (idempotency_key) DO NOTHING",
	} {
		if !strings.Contains(enqueueSiteTombstoneSQL, fragment) {
			t.Fatalf("site tombstone SQL missing %q: %s", fragment, enqueueSiteTombstoneSQL)
		}
	}
}
