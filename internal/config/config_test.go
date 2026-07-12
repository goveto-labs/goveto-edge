package config

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
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

func TestMasterKeyConcurrentInitialization(t *testing.T) {
	dir := t.TempDir()
	const instances = 16
	keys := make(chan string, instances)
	errs := make(chan error, instances)
	var ready sync.WaitGroup
	ready.Add(instances)
	start := make(chan struct{})
	for range instances {
		go func() {
			ready.Done()
			<-start
			key, err := loadOrCreateMasterKey(dir)
			keys <- key
			errs <- err
		}()
	}
	ready.Wait()
	close(start)

	var expected string
	for range instances {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		key := <-keys
		if expected == "" {
			expected = key
		} else if key != expected {
			t.Fatalf("concurrent instances received different keys")
		}
	}
}

func TestMasterKeyInterruptedWriteCanRecover(t *testing.T) {
	dir := t.TempDir()
	secretsDir := filepath.Join(dir, "secrets")
	if err := os.MkdirAll(secretsDir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(secretsDir, "node-credential-master.key")
	injected := errors.New("injected write failure")
	err := writeMasterKeyAtomically(path, "not-published", func(file *os.File, contents []byte) (int, error) {
		n, writeErr := file.Write(contents[:5])
		if writeErr != nil {
			return n, writeErr
		}
		return n, injected
	})
	if !errors.Is(err, injected) {
		t.Fatalf("expected injected failure, got %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed write published final key file: %v", err)
	}
	if _, err := loadOrCreateMasterKey(dir); err != nil {
		t.Fatalf("recover after interrupted write: %v", err)
	}
}

func TestMasterKeyExistingPermissionsAreTightened(t *testing.T) {
	dir := t.TempDir()
	key, err := loadOrCreateMasterKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "secrets", "node-credential-master.key")
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadOrCreateMasterKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded != key {
		t.Fatal("key changed while tightening permissions")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("permissions were not tightened: %o", info.Mode().Perm())
	}
}

func TestLoadValidationDoesNotCreateDataDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not-created")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("REDIS_URL", "redis://localhost:6379/0")
	t.Setenv("GOVETO_DATA_DIR", dir)
	if _, err := Load(); err == nil {
		t.Fatal("expected missing database URL error")
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("configuration validation created data directory: %v", err)
	}
}
