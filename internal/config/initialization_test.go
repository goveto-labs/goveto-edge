package config

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitializationTokenPersistsPrivately(t *testing.T) {
	t.Setenv("INIT_TOKEN", "")
	t.Setenv("INIT_TOKEN_FILE", "")
	dir := t.TempDir()
	first, err := InitializationToken(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := InitializationToken(dir)
	if err != nil || first != second || len(first) < 40 {
		t.Fatalf("token persistence: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "secrets", "initialization.token"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("token permissions: %v", err)
	}
}

func TestInitializationTokenFromEnv(t *testing.T) {
	token := base64.StdEncoding.EncodeToString(make([]byte, 32))
	t.Setenv("INIT_TOKEN", token)
	t.Setenv("INIT_TOKEN_FILE", "")
	got, err := InitializationToken(t.TempDir())
	if err != nil || got != token {
		t.Fatalf("token from INIT_TOKEN: got=%q err=%v", got, err)
	}
}

func TestInitializationTokenEnvTakesPrecedenceOverFile(t *testing.T) {
	token := base64.StdEncoding.EncodeToString(make([]byte, 32))
	fileToken := base64.StdEncoding.EncodeToString(bytes32(1))
	filePath := filepath.Join(t.TempDir(), "init-token")
	if err := os.WriteFile(filePath, []byte(fileToken), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("INIT_TOKEN", token)
	t.Setenv("INIT_TOKEN_FILE", filePath)
	got, err := InitializationToken(t.TempDir())
	if err != nil || got != token {
		t.Fatalf("INIT_TOKEN precedence: got=%q err=%v", got, err)
	}
}

func TestInitializationTokenFromEnvFile(t *testing.T) {
	token := base64.StdEncoding.EncodeToString(bytes32(2))
	filePath := filepath.Join(t.TempDir(), "init-token")
	if err := os.WriteFile(filePath, []byte("  "+token+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("INIT_TOKEN", "")
	t.Setenv("INIT_TOKEN_FILE", filePath)
	got, err := InitializationToken(t.TempDir())
	if err != nil || got != token {
		t.Fatalf("token from INIT_TOKEN_FILE: got=%q err=%v", got, err)
	}
}

func TestInitializationTokenRejectsInvalidFormat(t *testing.T) {
	t.Run("env", func(t *testing.T) {
		t.Setenv("INIT_TOKEN", "not-base64-32-bytes")
		t.Setenv("INIT_TOKEN_FILE", "")
		if _, err := InitializationToken(t.TempDir()); err == nil ||
			!strings.Contains(err.Error(), "initialization token in INIT_TOKEN must be base64-encoded 32 bytes") {
			t.Fatalf("expected format error, got %v", err)
		}
	})
	t.Run("file", func(t *testing.T) {
		filePath := filepath.Join(t.TempDir(), "init-token")
		if err := os.WriteFile(filePath, []byte("short"), 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("INIT_TOKEN", "")
		t.Setenv("INIT_TOKEN_FILE", filePath)
		if _, err := InitializationToken(t.TempDir()); err == nil ||
			!strings.Contains(err.Error(), "initialization token in INIT_TOKEN_FILE must be base64-encoded 32 bytes") {
			t.Fatalf("expected format error, got %v", err)
		}
	})
}

func bytes32(fill byte) []byte {
	raw := make([]byte, 32)
	for index := range raw {
		raw[index] = fill
	}
	return raw
}
