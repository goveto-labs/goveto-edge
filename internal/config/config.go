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
	AppEnv                         string
	HTTPHost                       string
	HTTPPort                       int
	HTTPReadHeaderTimeout          time.Duration
	HTTPReadTimeout                time.Duration
	HTTPWriteTimeout               time.Duration
	HTTPIdleTimeout                time.Duration
	HTTPMaxHeaderBytes             int
	HTTPMaxBodyBytes               int64
	HTTPMaxUploadBytes             int64
	AgentGatewayHost               string
	AgentGatewayPort               int
	ShutdownTimeout                time.Duration
	DatabaseURL                    string
	RedisURL                       string
	AnalyticsDatabaseURL           string
	AnalyticsDBMaxConns            int
	AnalyticsQueryTimeout          time.Duration
	AnalyticsIngestConcurrency     int
	AnalyticsRawRetentionDays      int
	AnalyticsArchiveDir            string
	AnalyticsArchiveS3Endpoint     string
	AnalyticsArchiveS3Bucket       string
	AnalyticsArchiveS3Region       string
	AnalyticsArchiveS3AccessKey    string
	AnalyticsArchiveS3SecretKey    string
	AnalyticsArchiveS3SessionToken string
	NodeCredentialMasterKey        string
	DataDir                        string
	SessionCookieName              string
	SessionTTL                     time.Duration
	SessionCookieSecure            bool
	GeoIPDatabasePath              string
	GeoIPDatabasePollInterval      time.Duration
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
	agentGatewayPort, err := envInt("AGENT_GATEWAY_PORT", 8443)
	if err != nil {
		return Config{}, err
	}
	readHeaderTimeout, err := envDuration("HTTP_READ_HEADER_TIMEOUT", 5*time.Second)
	if err != nil {
		return Config{}, err
	}
	readTimeout, err := envDuration("HTTP_READ_TIMEOUT", 15*time.Second)
	if err != nil {
		return Config{}, err
	}
	writeTimeout, err := envDuration("HTTP_WRITE_TIMEOUT", 30*time.Second)
	if err != nil {
		return Config{}, err
	}
	idleTimeout, err := envDuration("HTTP_IDLE_TIMEOUT", 60*time.Second)
	if err != nil {
		return Config{}, err
	}
	maxHeaderBytes, err := envInt("HTTP_MAX_HEADER_BYTES", 32<<10)
	if err != nil {
		return Config{}, err
	}
	maxBodyBytes, err := envInt("HTTP_MAX_BODY_BYTES", 2<<20)
	if err != nil {
		return Config{}, err
	}
	maxUploadBytes, err := envInt("HTTP_MAX_UPLOAD_BYTES", 16<<20)
	if err != nil {
		return Config{}, err
	}
	shutdownTimeout, err := envDuration("SHUTDOWN_TIMEOUT", 10*time.Second)
	if err != nil {
		return Config{}, err
	}
	analyticsIngestConcurrency, err := envInt("ANALYTICS_INGEST_CONCURRENCY", 4)
	if err != nil {
		return Config{}, err
	}
	analyticsRawRetentionDays, err := envInt("ANALYTICS_RAW_RETENTION_DAYS", 7)
	if err != nil {
		return Config{}, err
	}
	analyticsDBMaxConns, err := envInt("ANALYTICS_DB_MAX_CONNS", 16)
	if err != nil {
		return Config{}, err
	}
	analyticsQueryTimeout, err := envDuration("ANALYTICS_QUERY_TIMEOUT", 5*time.Second)
	if err != nil {
		return Config{}, err
	}

	appEnv := envString("APP_ENV", "development")
	geoIPPollInterval, err := envDuration("GEOIP_DATABASE_POLL_INTERVAL", 30*time.Second)
	if err != nil {
		return Config{}, err
	}
	defaultGeoIPPath := "./GeoLite2-City.mmdb"
	if appEnv != "development" && appEnv != "test" {
		defaultGeoIPPath = ""
	}
	defaultDataDir := ".data"
	if appEnv != "development" && appEnv != "test" {
		defaultDataDir = "/var/lib/goveto-edge"
	}
	cfg := Config{
		AppEnv:                         appEnv,
		DataDir:                        envString("GOVETO_DATA_DIR", defaultDataDir),
		HTTPHost:                       envString("HTTP_HOST", "0.0.0.0"),
		HTTPPort:                       port,
		HTTPReadHeaderTimeout:          readHeaderTimeout,
		HTTPReadTimeout:                readTimeout,
		HTTPWriteTimeout:               writeTimeout,
		HTTPIdleTimeout:                idleTimeout,
		HTTPMaxHeaderBytes:             maxHeaderBytes,
		HTTPMaxBodyBytes:               int64(maxBodyBytes),
		HTTPMaxUploadBytes:             int64(maxUploadBytes),
		AgentGatewayHost:               envString("AGENT_GATEWAY_HOST", "0.0.0.0"),
		AgentGatewayPort:               agentGatewayPort,
		ShutdownTimeout:                shutdownTimeout,
		DatabaseURL:                    os.Getenv("DATABASE_URL"),
		RedisURL:                       os.Getenv("REDIS_URL"),
		AnalyticsDatabaseURL:           strings.TrimSpace(os.Getenv("ANALYTICS_DATABASE_URL")),
		AnalyticsDBMaxConns:            analyticsDBMaxConns,
		AnalyticsQueryTimeout:          analyticsQueryTimeout,
		AnalyticsIngestConcurrency:     analyticsIngestConcurrency,
		AnalyticsRawRetentionDays:      analyticsRawRetentionDays,
		AnalyticsArchiveDir:            strings.TrimSpace(os.Getenv("ANALYTICS_ARCHIVE_DIR")),
		AnalyticsArchiveS3Endpoint:     strings.TrimSpace(os.Getenv("ANALYTICS_ARCHIVE_S3_ENDPOINT")),
		AnalyticsArchiveS3Bucket:       strings.TrimSpace(os.Getenv("ANALYTICS_ARCHIVE_S3_BUCKET")),
		AnalyticsArchiveS3Region:       strings.TrimSpace(os.Getenv("ANALYTICS_ARCHIVE_S3_REGION")),
		AnalyticsArchiveS3AccessKey:    strings.TrimSpace(os.Getenv("ANALYTICS_ARCHIVE_S3_ACCESS_KEY")),
		AnalyticsArchiveS3SecretKey:    strings.TrimSpace(os.Getenv("ANALYTICS_ARCHIVE_S3_SECRET_KEY")),
		AnalyticsArchiveS3SessionToken: strings.TrimSpace(os.Getenv("ANALYTICS_ARCHIVE_S3_SESSION_TOKEN")),
		SessionCookieName:              envString("SESSION_COOKIE_NAME", "goveto_session"),
		GeoIPDatabasePath:              strings.TrimSpace(envString("GEOIP_DATABASE_PATH", defaultGeoIPPath)),
		GeoIPDatabasePollInterval:      geoIPPollInterval,
		SessionCookieSecure:            envBool("SESSION_COOKIE_SECURE", false),
	}
	if strings.EqualFold(appEnv, "production") {
		cfg.SessionCookieSecure = true
	}
	cfg.SessionTTL, err = envDuration("SESSION_TTL", 24*time.Hour)
	if err != nil {
		return Config{}, err
	}
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}
	if cfg.AnalyticsDatabaseURL == "" {
		cfg.AnalyticsDatabaseURL = cfg.DatabaseURL
	}
	if cfg.RedisURL == "" {
		return Config{}, errors.New("REDIS_URL is required")
	}
	if cfg.HTTPPort < 1 || cfg.HTTPPort > 65535 {
		return Config{}, fmt.Errorf("HTTP_PORT must be between 1 and 65535")
	}
	if cfg.AgentGatewayPort < 1 || cfg.AgentGatewayPort > 65535 {
		return Config{}, fmt.Errorf("AGENT_GATEWAY_PORT must be between 1 and 65535")
	}
	if cfg.HTTPReadHeaderTimeout <= 0 || cfg.HTTPReadTimeout <= 0 || cfg.HTTPWriteTimeout <= 0 || cfg.HTTPIdleTimeout <= 0 {
		return Config{}, errors.New("HTTP timeouts must be positive")
	}
	if cfg.HTTPMaxHeaderBytes < 1024 || cfg.HTTPMaxBodyBytes < 1024 || cfg.HTTPMaxUploadBytes < cfg.HTTPMaxBodyBytes {
		return Config{}, errors.New("HTTP size limits are invalid")
	}
	if cfg.AnalyticsIngestConcurrency < 1 || cfg.AnalyticsIngestConcurrency > 64 {
		return Config{}, errors.New("ANALYTICS_INGEST_CONCURRENCY must be between 1 and 64")
	}
	if cfg.AnalyticsRawRetentionDays < 1 || cfg.AnalyticsRawRetentionDays > 3650 {
		return Config{}, errors.New("ANALYTICS_RAW_RETENTION_DAYS must be between 1 and 3650")
	}
	if cfg.AnalyticsDBMaxConns < 1 || cfg.AnalyticsDBMaxConns > 1024 {
		return Config{}, errors.New("ANALYTICS_DB_MAX_CONNS must be between 1 and 1024")
	}
	if cfg.AnalyticsQueryTimeout <= 0 {
		return Config{}, errors.New("ANALYTICS_QUERY_TIMEOUT must be positive")
	}
	if cfg.GeoIPDatabasePollInterval <= 0 {
		return Config{}, errors.New("GEOIP_DATABASE_POLL_INTERVAL must be positive")
	}
	if cfg.AnalyticsArchiveS3Endpoint != "" {
		if cfg.AnalyticsArchiveDir != "" {
			return Config{}, errors.New("ANALYTICS_ARCHIVE_DIR and ANALYTICS_ARCHIVE_S3_ENDPOINT are mutually exclusive")
		}
		if cfg.AnalyticsArchiveS3Bucket == "" || cfg.AnalyticsArchiveS3Region == "" ||
			cfg.AnalyticsArchiveS3AccessKey == "" || cfg.AnalyticsArchiveS3SecretKey == "" {
			return Config{}, errors.New("S3 analytics archive requires bucket, region, access key, and secret key")
		}
	}
	if configuredMasterKey := strings.TrimSpace(os.Getenv("NODE_CREDENTIAL_MASTER_KEY")); configuredMasterKey != "" {
		cfg.NodeCredentialMasterKey, err = validateMasterKey(configuredMasterKey, "NODE_CREDENTIAL_MASTER_KEY")
	} else {
		cfg.NodeCredentialMasterKey, err = loadOrCreateMasterKey(cfg.DataDir)
	}
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

func (c Config) AgentGatewayAddress() string {
	return fmt.Sprintf("%s:%d", c.AgentGatewayHost, c.AgentGatewayPort)
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
