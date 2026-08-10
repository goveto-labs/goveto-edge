package analytics

import (
	"strings"
	"testing"
	"time"
)

func TestWAFSeriesQuery30dParameterLayout(t *testing.T) {
	now := time.Date(2026, time.August, 10, 12, 0, 0, 0, time.UTC)

	withoutSite, withoutSiteArgs := wafSeriesQuery(now, "cluster", "", "30d")
	if len(withoutSiteArgs) != 6 {
		t.Fatalf("query without site has %d args, want 6", len(withoutSiteArgs))
	}
	if !strings.Contains(withoutSite, "cluster_id = $4 AND bucket >= $5 AND bucket < $6") {
		t.Fatalf("query without site has unexpected hourly parameters: %s", withoutSite)
	}

	withSite, withSiteArgs := wafSeriesQuery(now, "cluster", "site", "30d")
	if len(withSiteArgs) != 8 {
		t.Fatalf("query with site has %d args, want 8", len(withSiteArgs))
	}
	if !strings.Contains(withSite, "cluster_id = $5 AND bucket >= $6 AND bucket < $7 AND site_id = $8") {
		t.Fatalf("query with site has unexpected hourly parameters: %s", withSite)
	}
}
