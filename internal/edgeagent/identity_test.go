package edgeagent

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"goveto-edge/internal/edgecontrol"
	"goveto-edge/internal/node"
)

func TestWriteIdentityUsesPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent", "identity.json")
	expected := testIdentity(t)
	if err := WriteIdentity(path, expected); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("unexpected permissions: %o", info.Mode().Perm())
	}
	actual, err := LoadIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	if actual != expected {
		t.Fatalf("unexpected identity: %#v", actual)
	}
}

func TestWriteIdentityRejectsInvalidNodeID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.json")
	invalid := testIdentity(t)
	invalid.NodeID = "not-a-uuid"
	if err := WriteIdentity(path, invalid); err == nil {
		t.Fatal("expected invalid node id rejection")
	}
}

func TestPendingCredentialSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.json")
	first := &channelClient{identityPath: path, identity: testIdentity(t)}
	request, privateKey, err := first.prepareCredentialRequest()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	second := &channelClient{identityPath: path, identity: loaded}
	retried, retriedKey, err := second.prepareCredentialRequest()
	if err != nil {
		t.Fatal(err)
	}
	if retried.CSRPEM != request.CSRPEM {
		t.Fatal("credential retry generated a different CSR")
	}
	if !bytes.Equal(privateKey, retriedKey) {
		t.Fatal("credential retry loaded a different private key")
	}
}

func testIdentity(t *testing.T) Identity {
	t.Helper()
	cipher, err := node.NewCredentialCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	authority, err := edgecontrol.NewAuthority(cipher, "control.example:8443")
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := authority.IssueNode("550e8400-e29b-41d4-a716-446655440000")
	if err != nil {
		t.Fatal(err)
	}
	return Identity{
		NodeID: bundle.NodeID, GatewayAddress: bundle.GatewayAddress, ServerName: bundle.ServerName,
		CACertificate: bundle.CACertificate, Certificate: bundle.Certificate, PrivateKey: bundle.PrivateKey,
	}
}

func TestWriteIdentityRejectsIncomplete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.json")
	if err := WriteIdentity(path, Identity{NodeID: "550e8400-e29b-41d4-a716-446655440000"}); err == nil {
		t.Fatal("expected incomplete identity rejection")
	}
}

func TestLoadIdentityRejectsMissingFile(t *testing.T) {
	if _, err := LoadIdentity(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("expected missing file error")
	}
}

func TestLoadIdentityRejectsIncompleteJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.json")
	if err := os.WriteFile(path, []byte(`{"node_id":"550e8400-e29b-41d4-a716-446655440000"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadIdentity(path); err == nil {
		t.Fatal("expected incomplete identity error")
	}
}
