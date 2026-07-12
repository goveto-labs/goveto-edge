package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFromEnvironment(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgresql://localhost/goveto")
	t.Setenv("REDIS_URL", "redis://localhost:6379/0")
	t.Setenv("GOVETO_DATA_DIR", t.TempDir())
	t.Setenv("HTTP_PORT", "9090")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddress() != "0.0.0.0:9090" {
		t.Fatalf("unexpected HTTP address: %s", cfg.HTTPAddress())
	}
}

func TestMasterKeyGeneratedOnceAndReused(t *testing.T) {
	dir := t.TempDir()
	first, err := loadOrCreateMasterKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadOrCreateMasterKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("generated master key was not reused")
	}
	path := filepath.Join(dir, "secrets", "node-credential-master.key")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("unexpected key permissions: %o", info.Mode().Perm())
	}
}

func TestMasterKeyFileRejectsInvalidContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets", "node-credential-master.key")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreateMasterKey(dir); err == nil {
		t.Fatal("expected invalid key error")
	}
}
