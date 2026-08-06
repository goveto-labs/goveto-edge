package certmanager

import (
	"strings"
	"testing"

	"goveto-edge/internal/storage/gen/model"
)

func TestSelectBestDNSZone(t *testing.T) {
	zones := []model.DNSProviderConfig{
		{Id: "endpoint", Kind: model.DNSProviderKindENDPOINT, Zone: "example.com", Enabled: true},
		{Id: "acme-net", Kind: model.DNSProviderKindACME, Zone: "other.net", Enabled: true},
		{Id: "nested", Kind: model.DNSProviderKindACME, Zone: "cdn.example.com", Enabled: true},
		{Id: "disabled", Kind: model.DNSProviderKindACME, Zone: "disabled.com", Enabled: false},
	}

	tests := []struct {
		domain string
		want   string
		ok     bool
	}{
		{domain: "www.example.com", want: "endpoint", ok: true},
		{domain: "*.example.com", want: "endpoint", ok: true},
		{domain: "a.cdn.example.com", want: "nested", ok: true},
		{domain: "cdn.example.com", want: "nested", ok: true},
		{domain: "api.other.net", want: "acme-net", ok: true},
		{domain: "missing.org", ok: false},
		{domain: "disabled.com", ok: false},
	}

	for _, test := range tests {
		got, err := selectBestDNSZone(zones, test.domain)
		if test.ok {
			if err != nil {
				t.Fatalf("selectBestDNSZone(%q) unexpected error: %v", test.domain, err)
			}
			if got == nil || got.Id != test.want {
				t.Fatalf("selectBestDNSZone(%q) = %#v; want id %q", test.domain, got, test.want)
			}
			continue
		}
		if err == nil {
			t.Fatalf("selectBestDNSZone(%q) unexpectedly matched %#v", test.domain, got)
		}
		if !strings.Contains(err.Error(), "no DNS zone covers") && !strings.Contains(err.Error(), "no enabled DNS") {
			t.Fatalf("selectBestDNSZone(%q) error = %v", test.domain, err)
		}
	}
}
