package geoipdb

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectOfficialCityFixture(t *testing.T) {
	metadata, data, err := Inspect(filepath.Join("..", "testdata", "GeoIP2-City-Test.mmdb"))
	if err != nil {
		t.Fatal(err)
	}
	if metadata.SHA256 != "ed972738e4e03a3e56e12041a6af4d91592249d110f7e4a647e5f2fa0e639c09" {
		t.Fatalf("unexpected fixture checksum: %s", metadata.SHA256)
	}
	if metadata.Size != int64(len(data)) || metadata.BuildEpoch == 0 {
		t.Fatalf("unexpected fixture metadata: %#v", metadata)
	}
}

func TestInspectRejectsInvalidAndOversizedFiles(t *testing.T) {
	invalid := filepath.Join(t.TempDir(), "invalid.mmdb")
	if err := os.WriteFile(invalid, []byte("not a database"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Inspect(invalid); err == nil {
		t.Fatal("invalid database was accepted")
	}
	oversized := filepath.Join(t.TempDir(), "oversized.mmdb")
	file, err := os.Create(oversized)
	if err != nil {
		t.Fatal(err)
	}
	if err = file.Truncate(MaxSize + 1); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err = Inspect(oversized); err == nil || !strings.Contains(err.Error(), "outside the supported range") {
		t.Fatalf("unexpected oversized file result: %v", err)
	}
}
