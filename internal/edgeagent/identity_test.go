package edgeagent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteIdentityUsesPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent", "identity.json")
	expected := Identity{NodeID: "550e8400-e29b-41d4-a716-446655440000", CommunicationKey: "secret"}
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
	if err := WriteIdentity(path, Identity{NodeID: "not-a-uuid", CommunicationKey: "secret"}); err == nil {
		t.Fatal("expected invalid node id rejection")
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
