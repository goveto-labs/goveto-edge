package dns

import (
	"testing"

	"goveto-edge/internal/storage/gen/model"
)

func TestHostname(t *testing.T) {
	tests := []struct {
		input string
		want  string
		valid bool
	}{
		{input: "Edge.Example.com.", want: "edge.example.com", valid: true},
		{input: "bücher.example", want: "xn--bcher-kva.example", valid: true},
		{input: "*.example.com", valid: false},
		{input: "bad_label.example.com", valid: false},
		{input: "127.0.0.1", valid: false},
		{input: "bad..example.com", valid: false},
	}
	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			got, err := hostname(test.input)
			if test.valid {
				if err != nil || got != test.want {
					t.Fatalf("hostname(%q) = %q, %v; want %q", test.input, got, err, test.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("hostname(%q) unexpectedly returned %q", test.input, got)
			}
		})
	}
}

func TestValidProviderCode(t *testing.T) {
	for _, code := range []string{"telecom", "cn_mobile", "oversea-1"} {
		if !validProviderCode(code) {
			t.Fatalf("validProviderCode(%q) = false", code)
		}
	}
	for _, code := range []string{"", "China Telecom", "telecom/1"} {
		if validProviderCode(code) {
			t.Fatalf("validProviderCode(%q) = true", code)
		}
	}
}

func TestZoneUpdateRequiresValidation(t *testing.T) {
	zoneID := "zone-1"
	config := &model.DNSProviderConfig{
		Provider: model.DNSProviderTypeCLOUDFLARE,
		Zone:     "example.com",
		ZoneId:   &zoneID,
		Enabled:  true,
	}
	tests := []struct {
		name                string
		provider            model.DNSProviderType
		zone                string
		zoneID              string
		enabled             bool
		credentialsProvided bool
		want                bool
	}{
		{name: "disable only", provider: config.Provider, zone: config.Zone, zoneID: zoneID, enabled: false, want: false},
		{name: "keep enabled", provider: config.Provider, zone: config.Zone, zoneID: zoneID, enabled: true, want: true},
		{name: "replace credentials while disabled", provider: config.Provider, zone: config.Zone, zoneID: zoneID, enabled: false, credentialsProvided: true, want: true},
		{name: "change zone while disabled", provider: config.Provider, zone: "other.example.com", zoneID: zoneID, enabled: false, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := zoneUpdateRequiresValidation(config, test.provider, test.zone, test.zoneID, test.enabled, test.credentialsProvided)
			if got != test.want {
				t.Fatalf("zoneUpdateRequiresValidation() = %v; want %v", got, test.want)
			}
		})
	}
}
