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
		"ssh":{"entry_ip":"192.168.4.120","port":22,"user":"root","password":"secret"}
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
	if input.SSH.Password != "secret" || !input.SSH.UsesPassword() {
		t.Fatal("SSH password did not survive JSON binding")
	}
}

func TestInstallPayloadJSONPreservesPassword(t *testing.T) {
	want := InstallPayload{
		NodeID: "node-1",
		SSH: SSHInstallInput{
			EntryIP:  "192.168.4.120",
			Port:     22,
			User:     "root",
			Password: "secret",
		},
	}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got InstallPayload
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.SSH.Password != want.SSH.Password {
		t.Fatal("SSH password was lost while passing through the install queue")
	}
}
