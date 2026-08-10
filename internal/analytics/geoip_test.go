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

func TestGeoIPEnricherLabelsReservedAddressWithoutDatabase(t *testing.T) {
	enricher := newGeoIPEnricher(filepath.Join(t.TempDir(), "missing.mmdb"))
	events := []WebRequestLog{{ClientIP: netip.MustParseAddr("192.0.2.1")}}

	enricher.enrich(events)

	if events[0].Country != "" || events[0].Region != reservedNetworkRegion || events[0].ISP != reservedNetworkISP {
		t.Fatalf("unexpected GEO result: %#v", events[0])
	}
}

func TestGeoIPEnricherLabelsInternalNetworks(t *testing.T) {
	tests := []struct {
		name   string
		ip     string
		isp    string
		region string
	}{
		{name: "private IPv4", ip: "192.168.4.120", isp: localNetworkISP, region: localNetworkRegion},
		{name: "private IPv6", ip: "fd00::1", isp: localNetworkISP, region: localNetworkRegion},
		{name: "link local", ip: "169.254.1.1", isp: localNetworkISP, region: localNetworkRegion},
		{name: "loopback", ip: "127.0.0.1", isp: reservedNetworkISP, region: reservedNetworkRegion},
		{name: "shared address", ip: "100.64.0.1", isp: reservedNetworkISP, region: reservedNetworkRegion},
		{name: "benchmark", ip: "198.18.0.1", isp: reservedNetworkISP, region: reservedNetworkRegion},
		{name: "multicast", ip: "224.0.0.1", isp: reservedNetworkISP, region: reservedNetworkRegion},
	}

	enricher := newGeoIPEnricher("")
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			events := []WebRequestLog{{ClientIP: netip.MustParseAddr(test.ip)}}
			enricher.enrich(events)
			if events[0].ISP != test.isp || events[0].Region != test.region {
				t.Fatalf("unexpected labels for %s: isp=%q region=%q", test.ip, events[0].ISP, events[0].Region)
			}
		})
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
