// Package config loads application configuration from the environment.
package config

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
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
	cfg.NodeCredentialMasterKey, err = loadOrCreateMasterKey(cfg.DataDir)
	if err != nil {
		return Config{}, err
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
	if value, err := os.ReadFile(path); err == nil {
		return validateMasterKey(strings.TrimSpace(string(value)), path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read node credential master key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", fmt.Errorf("create secrets directory: %w", err)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate node credential master key: %w", err)
	}
	encoded := base64.StdEncoding.EncodeToString(raw)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		value, readErr := os.ReadFile(path)
		if readErr != nil {
			return "", fmt.Errorf("read concurrently created node credential master key: %w", readErr)
		}
		return validateMasterKey(strings.TrimSpace(string(value)), path)
	}
	if err != nil {
		return "", fmt.Errorf("create node credential master key: %w", err)
	}
	if _, err = file.WriteString(encoded + "\n"); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", fmt.Errorf("persist node credential master key: %w", err)
	}
	return encoded, nil
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
