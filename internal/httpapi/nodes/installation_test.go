package nodes

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestInstallationInfoOmitsConsumedBootstrapIdentity(t *testing.T) {
	data, err := json.Marshal(installationInfo{})
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(data)
	if strings.Contains(encoded, `"identity_json"`) {
		t.Fatalf("consumed bootstrap identity was exposed: %s", encoded)
	}
	if !strings.Contains(encoded, `"identity_available":false`) {
		t.Fatalf("identity availability is missing: %s", encoded)
	}
}

func TestInstallationInfoIncludesAvailableBootstrapIdentity(t *testing.T) {
	data, err := json.Marshal(installationInfo{IdentityJSON: `{"node_id":"node-1"}`, IdentityAvailable: true})
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(data)
	if !strings.Contains(encoded, `"identity_json"`) || !strings.Contains(encoded, `"identity_available":true`) {
		t.Fatalf("available bootstrap identity is missing: %s", encoded)
	}
}
