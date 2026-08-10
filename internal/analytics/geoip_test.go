package analytics

import (
	"net/netip"
	"path/filepath"
	"testing"
)

func TestGeoIPEnricherWritesCountryAndRegion(t *testing.T) {
	enricher := newGeoIPEnricher(filepath.Join("..", "testdata", "GeoIP2-City-Test.mmdb"))
	address := netip.MustParseAddr("81.2.69.160")
	events := []WebRequestLog{{ClientIP: netip.AddrFrom16(address.As16())}}

	enricher.enrich(events)

	if events[0].Country != "GB" || events[0].Region != "GB-ENG" {
		t.Fatalf("unexpected GEO result: country=%q region=%q", events[0].Country, events[0].Region)
	}
}

func TestGeoIPEnricherDoesNotBlockIngestWithoutDatabase(t *testing.T) {
	enricher := newGeoIPEnricher(filepath.Join(t.TempDir(), "missing.mmdb"))
	events := []WebRequestLog{{ClientIP: netip.MustParseAddr("192.0.2.1")}}

	enricher.enrich(events)

	if events[0].Country != "" || events[0].Region != "" {
		t.Fatalf("unexpected GEO result: %#v", events[0])
	}
}

func TestFormatISP(t *testing.T) {
	if got := formatISP(13335, "", "Cloudflare, Inc."); got != "AS13335 · Cloudflare, Inc." {
		t.Fatalf("unexpected ISP label: %q", got)
	}
	if got := formatISP(0, "Example ISP", "Example Org"); got != "Example ISP" {
		t.Fatalf("unexpected ISP-only label: %q", got)
	}
}
