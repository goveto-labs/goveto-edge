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
