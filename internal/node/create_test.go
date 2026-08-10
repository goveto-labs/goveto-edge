package node

import (
	"encoding/json"
	"testing"
)

func TestCreateInputJSONContract(t *testing.T) {
	raw := []byte(`{
		"name":"edge-1",
		"addresses":["192.168.4.120"],
		"dns_line_ids":["line-default","line-telecom"],
		"group_ids":[],
		"region_ids":[],
		"ssh":{"entry_ip":"192.168.4.120","port":22,"credential_id":"credential-1"}
	}`)
	var input CreateInput
	if err := json.Unmarshal(raw, &input); err != nil {
		t.Fatal(err)
	}
	input.ClusterID = "cluster-1"
	if err := input.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(input.Addresses) != 1 || input.Addresses[0] != "192.168.4.120" {
		t.Fatalf("addresses=%#v", input.Addresses)
	}
	if len(input.DNSLineIDs) != 2 || input.DNSLineIDs[1] != "line-telecom" {
		t.Fatalf("dns_line_ids=%#v", input.DNSLineIDs)
	}
	if input.SSH.CredentialID != "credential-1" {
		t.Fatal("SSH credential reference did not survive JSON binding")
	}
}

func TestCreateInputCanonicalizesAndDeduplicatesAddresses(t *testing.T) {
	input := CreateInput{
		ClusterID: "cluster-1",
		Name:      "edge-1",
		Addresses: []string{"2001:0db8:0:0::1", "2001:db8::1"},
		SSH: SSHInstallReference{
			EntryIP: "192.0.2.1", Port: 22, CredentialID: "credential-1",
		},
	}
	if err := input.Validate(); err == nil {
		t.Fatal("equivalent IPv6 addresses were not rejected as duplicates")
	}

	input.Addresses = []string{"2001:0db8:0:0::1"}
	if err := input.Validate(); err != nil {
		t.Fatal(err)
	}
	if input.Addresses[0] != "2001:db8::1" {
		t.Fatalf("canonical address = %q", input.Addresses[0])
	}
}

func TestInstallPayloadJSONContainsOnlyNodeID(t *testing.T) {
	want := InstallPayload{NodeID: "node-1"}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got InstallPayload
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.NodeID != want.NodeID || got.SSH != nil || got.IdentityJSON != "" {
		t.Fatalf("unexpected install payload: %#v", got)
	}
	if string(raw) != `{"node_id":"node-1"}` {
		t.Fatalf("install payload contains unexpected data: %s", raw)
	}
}
