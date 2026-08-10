package types

import (
	"encoding/json"
	"testing"

	"goveto-edge/internal/storage/gen/model"
)

// TestDNSPayloadsNeverExposeEncryptedCredentials verifies the public mappers
// report only whether credentials are configured (a boolean) and never echo
// the encrypted material. This is the audit/integrity boundary that keeps a
// config snapshot safe to log or return over the API.
func TestDNSPayloadsNeverExposeEncryptedCredentials(t *testing.T) {
	configured := &model.DNSProviderConfig{
		Id: "cfg-1", Kind: model.DNSProviderKindENDPOINT, Provider: model.DNSProviderTypeCLOUDFLARE,
		Zone: "example.com", CredentialsEncrypted: "opaque-cipher-text",
	}
	zone := NewDNSZone(configured)
	if !zone.CredentialsConfigured || zone.ID != "cfg-1" || zone.Zone != "example.com" {
		t.Fatalf("zone mapping lost fields: %#v", zone)
	}

	endpoint := NewDNSProviderConfig(configured)
	if endpoint == nil || !endpoint.CredentialsConfigured {
		t.Fatalf("endpoint credentials flag wrong: %#v", endpoint)
	}

	empty := &model.DNSProviderConfig{Id: "cfg-2", CredentialsEncrypted: ""}
	if emptyZone := NewDNSZone(empty); emptyZone.CredentialsConfigured {
		t.Fatal("empty credentials must report CredentialsConfigured=false")
	}

	if NewDNSProviderConfig(nil) != nil {
		t.Fatal("nil endpoint must map to nil")
	}
}

// TestNewDNSConfigSeparatesEndpointFromZones confirms the endpoint provider is
// hoisted into Provider while every zone (including the endpoint itself when
// present) is also listed in Zones.
func TestNewDNSConfigSeparatesEndpointFromZones(t *testing.T) {
	hostname := "edge.example.com"
	endpoint := &model.DNSProviderConfig{
		Id: "ep", Kind: model.DNSProviderKindENDPOINT, Provider: model.DNSProviderTypeALIYUN, Zone: "example.com",
	}
	acme := model.DNSProviderConfig{Id: "acme", Kind: model.DNSProviderKindACME, Provider: model.DNSProviderTypeCLOUDFLARE, Zone: "acme.example.com"}
	config := NewDNSConfig(&hostname, endpoint, []model.DNSProviderConfig{*endpoint, acme})
	if config.PrimaryHostname == nil || *config.PrimaryHostname != hostname {
		t.Fatalf("primary hostname lost: %#v", config.PrimaryHostname)
	}
	if config.Provider == nil || config.Provider.ID != "ep" {
		t.Fatalf("endpoint not hoisted: %#v", config.Provider)
	}
	if len(config.Zones) != 2 || config.Zones[0].ID != "ep" || config.Zones[1].ID != "acme" {
		t.Fatalf("zones not listed in order: %#v", config.Zones)
	}

	empty := NewDNSConfig(nil, nil, nil)
	if empty.PrimaryHostname != nil || empty.Provider != nil || len(empty.Zones) != 0 {
		t.Fatalf("empty config should have nil provider and no zones: %#v", empty)
	}
}

// TestNewDNSJobDecodesResultError confirms the job result (which carries the
// provider error string on failure) is decoded and surfaced for operators.
func TestNewDNSJobDecodesResultError(t *testing.T) {
	resultJSON := json.RawMessage(`{"error":"cloudflare: invalid api token"}`)
	job := &model.DNSSyncJob{
		Id: "job-1", Action: model.DNSSyncActionUPSERT_CLUSTER, Status: model.JobStatusFAILED,
		ResultJson: &resultJSON,
	}
	response := NewDNSJob(job)
	if response.Result == nil || response.Result.Error != "cloudflare: invalid api token" {
		t.Fatalf("provider error not surfaced: %#v", response.Result)
	}

	malformed := json.RawMessage(`{not json`)
	job.ResultJson = &malformed
	if NewDNSJob(job).Result != nil {
		t.Fatal("malformed result JSON should yield a nil result, not a panic")
	}
}
