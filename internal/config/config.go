// Package config loads application configuration from the environment.
package config

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"golang.org/x/sys/unix"
)

type Config struct {
	AppEnv                  string
	HTTPHost                string
	HTTPPort                int
	HTTPReadHeaderTimeout   time.Duration
	ShutdownTimeout         time.Duration
	DatabaseURL             string
	RedisURL                string
	ClickHouseDSN           string
	NodeCredentialMasterKey string
	DataDir                 string
	SessionCookieName       string
	SessionTTL              time.Duration
	SessionCookieSecure     bool
}

// Load reads .env when present, then reads configuration from the process
// environment. Existing environment variables always take precedence.
func Load() (Config, error) {
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Config{}, fmt.Errorf("load .env: %w", err)
	}

	port, err := envInt("HTTP_PORT", 8080)
	if err != nil {
		return Config{}, err
	}
	readHeaderTimeout, err := envDuration("HTTP_READ_HEADER_TIMEOUT", 5*time.Second)
	if err != nil {
		return Config{}, err
	}
	shutdownTimeout, err := envDuration("SHUTDOWN_TIMEOUT", 10*time.Second)
	if err != nil {
		return Config{}, err
	}

	appEnv := envString("APP_ENV", "development")
	defaultDataDir := ".data"
	if appEnv != "development" && appEnv != "test" {
		defaultDataDir = "/var/lib/goveto-edge"
	}
	cfg := Config{
		AppEnv:                appEnv,
		DataDir:               envString("GOVETO_DATA_DIR", defaultDataDir),
		HTTPHost:              envString("HTTP_HOST", "0.0.0.0"),
		HTTPPort:              port,
		HTTPReadHeaderTimeout: readHeaderTimeout,
		ShutdownTimeout:       shutdownTimeout,
		DatabaseURL:           os.Getenv("DATABASE_URL"),
		RedisURL:              os.Getenv("REDIS_URL"),
		ClickHouseDSN:         os.Getenv("CLICKHOUSE_DSN"),
		SessionCookieName:     envString("SESSION_COOKIE_NAME", "goveto_session"),
		SessionCookieSecure:   envBool("SESSION_COOKIE_SECURE", false),
	}
	cfg.SessionTTL, err = envDuration("SESSION_TTL", 24*time.Hour)
	if err != nil {
		return Config{}, err
	}
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}
	if cfg.RedisURL == "" {
		return Config{}, errors.New("REDIS_URL is required")
	}
	if cfg.HTTPPort < 1 || cfg.HTTPPort > 65535 {
		return Config{}, fmt.Errorf("HTTP_PORT must be between 1 and 65535")
	}
	cfg.NodeCredentialMasterKey, err = loadOrCreateMasterKey(cfg.DataDir)
	if err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func envBool(key string, fallback bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func (c Config) HTTPAddress() string {
	return fmt.Sprintf("%s:%d", c.HTTPHost, c.HTTPPort)
}

func loadOrCreateMasterKey(dataDir string) (string, error) {
	path := filepath.Join(dataDir, "secrets", "node-credential-master.key")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", fmt.Errorf("create secrets directory: %w", err)
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return "", fmt.Errorf("open node credential master key lock: %w", err)
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return "", fmt.Errorf("lock node credential master key: %w", err)
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN) //nolint:errcheck

	if value, err := readMasterKey(path); err == nil {
		return value, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate node credential master key: %w", err)
	}
	encoded := base64.StdEncoding.EncodeToString(raw)
	if err := writeMasterKeyAtomically(path, encoded, (*os.File).Write); err != nil {
		return "", fmt.Errorf("persist node credential master key: %w", err)
	}
	return encoded, nil
}

func readMasterKey(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", os.ErrNotExist
		}
		return "", fmt.Errorf("stat node credential master key: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", fmt.Errorf("node credential master key %s must be a regular file, not a symlink", path)
	}
	if info.Mode().Perm() != 0600 {
		if err := os.Chmod(path, 0600); err != nil {
			return "", fmt.Errorf("secure node credential master key permissions: %w", err)
		}
	}
	value, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read node credential master key: %w", err)
	}
	return validateMasterKey(strings.TrimSpace(string(value)), path)
}

func writeMasterKeyAtomically(path, value string, write func(*os.File, []byte) (int, error)) (err error) {
	dir := filepath.Dir(path)
	temporary, err := os.CreateTemp(dir, ".node-credential-master.key-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() {
		temporary.Close()
		os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(0600); err != nil {
		return err
	}
	contents := []byte(value + "\n")
	if n, err := write(temporary, contents); err != nil {
		return err
	} else if n != len(contents) {
		return io.ErrShortWrite
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func validateMasterKey(value, path string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(raw) != 32 {
		return "", fmt.Errorf("node credential master key in %s must be base64-encoded 32 bytes", path)
	}
	return value, nil
}

func envString(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) (int, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", key, err)
	}
	return parsed, nil
}

func envDuration(key string, fallback time.Duration) (time.Duration, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration: %w", key, err)
	}
	return parsed, nil
}
