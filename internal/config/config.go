// Package config loads application configuration from the environment.
package config

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"golang.org/x/sys/unix"

	"goveto-edge/internal/outboundhttp"
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
	AlertEvalInterval              time.Duration
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
	NodeCredentialPreviousKeys     []string
	CertificateMasterKey           string
	CertificatePreviousKeys        []string
	ConfigSecretMasterKey          string
	ConfigSecretPreviousKeys       []string
	DNSCredentialMasterKey         string
	DNSCredentialPreviousKeys      []string
	NotificationMasterKey          string
	NotificationPreviousKeys       []string
	TOTPMasterKey                  string
	TOTPPreviousKeys               []string
	AgentCAMasterKey               string
	AgentCAMasterKeyPinned         bool
	OutboundPrivateAllowlist       []netip.Prefix
	DataDir                        string
	SessionCookieName              string
	SessionTTL                     time.Duration
	SessionCookieSecure            bool
	GeoIPDatabasePath              string
	GeoIPASNDatabasePath           string
	GeoIPDatabasePollInterval      time.Duration
	MetricsEnabled                 bool
	LogpushEnabled                 bool
	LogpushQueueSize               int
	LogpushBatchLinger             time.Duration
	// DNSMinHealthyTime is nil when DNS_MIN_HEALTHY_TIME is unset, so the DNS
	// scheduler applies its default; an explicit zero disables the guard.
	DNSMinHealthyTime    *time.Duration
	DNSMaxRemovalRatio   float64
	DNSMinPublishedNodes int
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
	alertEvalInterval, err := envDuration("ALERT_EVAL_INTERVAL", 30*time.Second)
	if err != nil {
		return Config{}, err
	}
	if alertEvalInterval < 5*time.Second || alertEvalInterval > 10*time.Minute {
		return Config{}, errors.New("ALERT_EVAL_INTERVAL must be between 5s and 10m")
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
	logpushQueueSize, err := envInt("LOGPUSH_QUEUE_SIZE", 4096)
	if err != nil {
		return Config{}, err
	}
	logpushBatchLinger, err := envDuration("LOGPUSH_BATCH_LINGER", 500*time.Millisecond)
	if err != nil {
		return Config{}, err
	}

	appEnv := envString("APP_ENV", "development")
	geoIPPollInterval, err := envDuration("GEOIP_DATABASE_POLL_INTERVAL", 30*time.Second)
	if err != nil {
		return Config{}, err
	}
	defaultGeoIPPath := "./GeoLite2-City.mmdb"
	defaultGeoIPASNPath := "./GeoLite2-ASN.mmdb"
	if appEnv != "development" && appEnv != "test" {
		defaultGeoIPPath = ""
		defaultGeoIPASNPath = ""
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
		AlertEvalInterval:              alertEvalInterval,
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
		GeoIPASNDatabasePath:           strings.TrimSpace(envString("GEOIP_ASN_DATABASE_PATH", defaultGeoIPASNPath)),
		GeoIPDatabasePollInterval:      geoIPPollInterval,
		SessionCookieSecure:            envBool("SESSION_COOKIE_SECURE", false),
		MetricsEnabled:                 envBool("METRICS_ENABLED", false),
		LogpushEnabled:                 envBool("LOGPUSH_ENABLED", true),
		LogpushQueueSize:               logpushQueueSize,
		LogpushBatchLinger:             logpushBatchLinger,
	}
	dnsMinHealthyTime, err := envOptionalDuration("DNS_MIN_HEALTHY_TIME")
	if err != nil {
		return Config{}, err
	}
	dnsMaxRemovalRatio, err := envFloat("DNS_MAX_REMOVAL_RATIO", 0.34)
	if err != nil {
		return Config{}, err
	}
	dnsMinPublishedNodes, err := envInt("DNS_MIN_PUBLISHED_NODES", 2)
	if err != nil {
		return Config{}, err
	}
	cfg.DNSMinHealthyTime = dnsMinHealthyTime
	cfg.DNSMaxRemovalRatio = dnsMaxRemovalRatio
	cfg.DNSMinPublishedNodes = dnsMinPublishedNodes
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
	if cfg.LogpushQueueSize < 1 || cfg.LogpushQueueSize > 65536 {
		return Config{}, errors.New("LOGPUSH_QUEUE_SIZE must be between 1 and 65536")
	}
	if cfg.LogpushBatchLinger <= 0 {
		return Config{}, errors.New("LOGPUSH_BATCH_LINGER must be positive")
	}
	if cfg.GeoIPDatabasePollInterval <= 0 {
		return Config{}, errors.New("GEOIP_DATABASE_POLL_INTERVAL must be positive")
	}
	if cfg.DNSMinHealthyTime != nil && *cfg.DNSMinHealthyTime < 0 {
		return Config{}, errors.New("DNS_MIN_HEALTHY_TIME must not be negative")
	}
	if cfg.DNSMaxRemovalRatio <= 0 || cfg.DNSMaxRemovalRatio > 1 {
		return Config{}, errors.New("DNS_MAX_REMOVAL_RATIO must be between 0 (exclusive) and 1 (inclusive)")
	}
	if cfg.DNSMinPublishedNodes < 1 {
		return Config{}, errors.New("DNS_MIN_PUBLISHED_NODES must be at least 1")
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
	configuredMasterKey, externallyConfigured, configuredErr := configuredKey("NODE_CREDENTIAL_MASTER_KEY")
	if configuredErr != nil {
		return Config{}, configuredErr
	}
	if externallyConfigured {
		cfg.NodeCredentialMasterKey = configuredMasterKey
	} else {
		cfg.NodeCredentialMasterKey, err = loadOrCreateMasterKey(cfg.DataDir)
	}
	if err != nil {
		return Config{}, err
	}
	if cfg.NodeCredentialPreviousKeys, err = previousMasterKeys("NODE_CREDENTIAL_PREVIOUS_KEYS"); err != nil {
		return Config{}, err
	}
	persistPurposeKeys := !externallyConfigured
	if cfg.CertificateMasterKey, err = purposeMasterKey(cfg.DataDir, "CERTIFICATE_MASTER_KEY", "certificate-master.key", cfg.NodeCredentialMasterKey, "goveto-edge/secrets/certificate/v1", persistPurposeKeys); err != nil {
		return Config{}, err
	}
	if cfg.CertificatePreviousKeys, err = purposePreviousKeys("CERTIFICATE_PREVIOUS_KEYS", cfg.NodeCredentialPreviousKeys, "goveto-edge/secrets/certificate/v1"); err != nil {
		return Config{}, err
	}
	if cfg.ConfigSecretMasterKey, err = purposeMasterKey(cfg.DataDir, "CONFIG_SECRET_MASTER_KEY", "config-secret-master.key", cfg.NodeCredentialMasterKey, "goveto-edge/secrets/config/v1", persistPurposeKeys); err != nil {
		return Config{}, err
	}
	if cfg.ConfigSecretPreviousKeys, err = purposePreviousKeys("CONFIG_SECRET_PREVIOUS_KEYS", cfg.NodeCredentialPreviousKeys, "goveto-edge/secrets/config/v1"); err != nil {
		return Config{}, err
	}
	if cfg.DNSCredentialMasterKey, err = purposeMasterKey(cfg.DataDir, "DNS_CREDENTIAL_MASTER_KEY", "dns-credential-master.key", cfg.NodeCredentialMasterKey, "goveto-edge/secrets/dns/v1", persistPurposeKeys); err != nil {
		return Config{}, err
	}
	if cfg.DNSCredentialPreviousKeys, err = purposePreviousKeys("DNS_CREDENTIAL_PREVIOUS_KEYS", cfg.NodeCredentialPreviousKeys, "goveto-edge/secrets/dns/v1"); err != nil {
		return Config{}, err
	}
	if cfg.NotificationMasterKey, err = purposeMasterKey(cfg.DataDir, "NOTIFICATION_MASTER_KEY", "notification-master.key", cfg.NodeCredentialMasterKey, "goveto-edge/secrets/notification/v1", persistPurposeKeys); err != nil {
		return Config{}, err
	}
	if cfg.NotificationPreviousKeys, err = purposePreviousKeys("NOTIFICATION_PREVIOUS_KEYS", cfg.NodeCredentialPreviousKeys, "goveto-edge/secrets/notification/v1"); err != nil {
		return Config{}, err
	}
	if cfg.TOTPMasterKey, err = purposeMasterKey(cfg.DataDir, "TOTP_MASTER_KEY", "totp-master.key", cfg.NodeCredentialMasterKey, "goveto-edge/secrets/totp/v1", persistPurposeKeys); err != nil {
		return Config{}, err
	}
	if cfg.TOTPPreviousKeys, err = purposePreviousKeys("TOTP_PREVIOUS_KEYS", cfg.NodeCredentialPreviousKeys, "goveto-edge/secrets/totp/v1"); err != nil {
		return Config{}, err
	}
	legacyCAKey := deriveEncodedMasterKey(cfg.NodeCredentialMasterKey, "goveto-edge/agent-mtls/ca/v1")
	if cfg.AgentCAMasterKey, err = purposeMasterKey(cfg.DataDir, "AGENT_CA_MASTER_KEY", "agent-ca-master.key", legacyCAKey, "", persistPurposeKeys); err != nil {
		return Config{}, err
	}
	cfg.AgentCAMasterKeyPinned = keyConfigured("AGENT_CA_MASTER_KEY")
	if raw, configured := os.LookupEnv("OUTBOUND_PRIVATE_ALLOWLIST"); configured {
		parsed, parseErr := outboundhttp.ParseAllowlist(raw)
		if parseErr != nil {
			return Config{}, parseErr
		}
		cfg.OutboundPrivateAllowlist = parsed
	} else {
		cfg.OutboundPrivateAllowlist = outboundhttp.DefaultPrivateAllowlist()
	}
	return cfg, nil
}

func purposeMasterKey(dataDir, envName, fileName, rootKey, label string, persist bool) (string, error) {
	if configured, ok, err := configuredKey(envName); err != nil {
		return "", err
	} else if ok {
		return configured, nil
	}
	derived := rootKey
	if label != "" {
		derived = deriveEncodedMasterKey(rootKey, label)
	}
	if !persist {
		return derived, nil
	}
	return loadOrMigrateNamedMasterKey(dataDir, fileName, derived, keyFingerprint(rootKey))
}

func configuredKey(envName string) (string, bool, error) {
	if value := strings.TrimSpace(os.Getenv(envName)); value != "" {
		key, err := validateMasterKey(value, envName)
		return key, true, err
	}
	fileName := strings.TrimSpace(os.Getenv(envName + "_FILE"))
	if fileName == "" {
		return "", false, nil
	}
	value, err := os.ReadFile(fileName)
	if err != nil {
		return "", false, fmt.Errorf("read %s_FILE: %w", envName, err)
	}
	key, err := validateMasterKey(strings.TrimSpace(string(value)), envName+"_FILE")
	return key, true, err
}

func purposePreviousKeys(envName string, rootPrevious []string, label string) ([]string, error) {
	explicit, err := previousMasterKeys(envName)
	if err != nil {
		return nil, err
	}
	for _, previous := range rootPrevious {
		explicit = append(explicit, deriveEncodedMasterKey(previous, label))
	}
	return explicit, nil
}

func previousMasterKeys(envName string) ([]string, error) {
	value := strings.TrimSpace(os.Getenv(envName))
	if value == "" {
		fileName := strings.TrimSpace(os.Getenv(envName + "_FILE"))
		if fileName != "" {
			contents, err := os.ReadFile(fileName)
			if err != nil {
				return nil, fmt.Errorf("read %s_FILE: %w", envName, err)
			}
			value = strings.TrimSpace(string(contents))
		}
	}
	if value == "" {
		return nil, nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for index, part := range parts {
		key, err := validateMasterKey(strings.TrimSpace(part), fmt.Sprintf("%s[%d]", envName, index))
		if err != nil {
			return nil, err
		}
		result = append(result, key)
	}
	return result, nil
}

func deriveEncodedMasterKey(encodedRoot, label string) string {
	root, _ := base64.StdEncoding.DecodeString(encodedRoot)
	mac := hmac.New(sha256.New, root)
	_, _ = io.WriteString(mac, label)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
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
	return loadOrCreateNamedMasterKey(dataDir, "node-credential-master.key", "")
}

// InitializationToken is only loaded while the instance is uninitialized.
// Supply INIT_TOKEN (or INIT_TOKEN_FILE) identically on multiple replicas.
// A configured token must be base64-encoded 32 bytes, the same format as
// master keys.
func InitializationToken(dataDir string) (string, error) {
	if token, configured, err := configuredInitializationToken("INIT_TOKEN"); configured || err != nil {
		return token, err
	}
	return loadOrCreateNamedMasterKey(dataDir, "initialization.token", "")
}

// configuredInitializationToken mirrors configuredKey but validates with
// token-specific error messages so operators are not pointed at master key
// configuration.
func configuredInitializationToken(envName string) (string, bool, error) {
	if value := strings.TrimSpace(os.Getenv(envName)); value != "" {
		token, err := validateInitializationToken(value, envName)
		return token, true, err
	}
	fileName := strings.TrimSpace(os.Getenv(envName + "_FILE"))
	if fileName == "" {
		return "", false, nil
	}
	value, err := os.ReadFile(fileName)
	if err != nil {
		return "", false, fmt.Errorf("read %s_FILE: %w", envName, err)
	}
	token, err := validateInitializationToken(strings.TrimSpace(string(value)), envName+"_FILE")
	return token, true, err
}

func validateInitializationToken(value, path string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(raw) != 32 {
		return "", fmt.Errorf("initialization token in %s must be base64-encoded 32 bytes", path)
	}
	return value, nil
}

func loadOrCreateNamedMasterKey(dataDir, fileName, initialValue string) (string, error) {
	path := filepath.Join(dataDir, "secrets", fileName)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", fmt.Errorf("create secrets directory: %w", err)
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return "", fmt.Errorf("open master key lock: %w", err)
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return "", fmt.Errorf("lock master key: %w", err)
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN) //nolint:errcheck

	if value, err := readMasterKey(path); err == nil {
		return value, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	encoded := initialValue
	if encoded == "" {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return "", fmt.Errorf("generate master key: %w", err)
		}
		encoded = base64.StdEncoding.EncodeToString(raw)
	}
	if err := writeMasterKeyAtomically(path, encoded, (*os.File).Write); err != nil {
		return "", fmt.Errorf("persist master key: %w", err)
	}
	return encoded, nil
}

// loadOrMigrateNamedMasterKey persists a purpose-specific master key that is a
// deterministic function of rootKey (the certificate, DNS, notification,
// TOTP, agent CA, and config-secret purpose keys). Unlike the shared root key these values must be
// re-derived and overwritten when the root key rotates; otherwise callers keep
// reading the value derived from the previous root and Rewrap becomes a no-op
// (defeating the purpose of NODE_CREDENTIAL_PREVIOUS_KEYS). The companion
// ".source" file records keyFingerprint(rootKey); on mismatch (or a legacy key
// file written without one) the value is re-derived, compared, and rewritten
// when it changed. Rewrap then migrates ciphertexts, which requires the
// previous root key to be present in the matching *_PREVIOUS_KEYS keyring.
func loadOrMigrateNamedMasterKey(dataDir, fileName, derivedValue, sourceFingerprint string) (string, error) {
	path := filepath.Join(dataDir, "secrets", fileName)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", fmt.Errorf("create secrets directory: %w", err)
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return "", fmt.Errorf("open master key lock: %w", err)
	}
	defer lock.Close()
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return "", fmt.Errorf("lock master key: %w", err)
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN) //nolint:errcheck

	existing, readErr := readMasterKey(path)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return "", readErr
	}
	companionPath := path + ".source"
	if readErr == nil {
		stored, _ := os.ReadFile(companionPath)
		if strings.TrimSpace(string(stored)) == sourceFingerprint {
			return existing, nil
		}
		if existing == derivedValue {
			// Legacy key file predating source tracking (or a no-op rotation):
			// the persisted value already matches the current root key, so only
			// backfill the companion.
			if err = writeMasterKeyAtomically(companionPath, sourceFingerprint, (*os.File).Write); err != nil {
				return "", fmt.Errorf("persist master key source: %w", err)
			}
			return existing, nil
		}
		slog.Warn("rotating persisted purpose master key after root key change; ensure the previous root key is listed in the matching PREVIOUS_KEYS setting", "key", fileName)
		if err = writeMasterKeyAtomically(path, derivedValue, (*os.File).Write); err != nil {
			return "", fmt.Errorf("rotate master key: %w", err)
		}
		if err = writeMasterKeyAtomically(companionPath, sourceFingerprint, (*os.File).Write); err != nil {
			return "", fmt.Errorf("persist master key source: %w", err)
		}
		return derivedValue, nil
	}
	if err = writeMasterKeyAtomically(path, derivedValue, (*os.File).Write); err != nil {
		return "", fmt.Errorf("persist master key: %w", err)
	}
	if err = writeMasterKeyAtomically(companionPath, sourceFingerprint, (*os.File).Write); err != nil {
		return "", fmt.Errorf("persist master key source: %w", err)
	}
	return derivedValue, nil
}

// keyFingerprint returns a short, stable identifier for an encoded master key
// so persisted purpose keys can record which root key produced them.
func keyFingerprint(encodedKey string) string {
	decoded, err := base64.StdEncoding.DecodeString(encodedKey)
	if err != nil {
		decoded = []byte(encodedKey)
	}
	sum := sha256.Sum256(decoded)
	return hex.EncodeToString(sum[:8])
}

// keyConfigured reports whether envName or its _FILE companion is set, without
// validating the value. It distinguishes a deliberately pinned key (for example
// AGENT_CA_MASTER_KEY) from one derived from the shared credential master key.
func keyConfigured(envName string) bool {
	if strings.TrimSpace(os.Getenv(envName)) != "" {
		return true
	}
	return strings.TrimSpace(os.Getenv(envName+"_FILE")) != ""
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
		return "", fmt.Errorf("master key %s must be a regular file, not a symlink", path)
	}
	if info.Mode().Perm() != 0600 {
		if err := os.Chmod(path, 0600); err != nil {
			return "", fmt.Errorf("secure node credential master key permissions: %w", err)
		}
	}
	value, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read master key: %w", err)
	}
	return validateMasterKey(strings.TrimSpace(string(value)), path)
}

func writeMasterKeyAtomically(path, value string, write func(*os.File, []byte) (int, error)) (err error) {
	dir := filepath.Dir(path)
	temporary, err := os.CreateTemp(dir, ".master-key-*")
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
		return "", fmt.Errorf("master key in %s must be base64-encoded 32 bytes", path)
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

// envOptionalDuration parses a duration env var and returns nil when it is
// unset, so callers can distinguish "not configured" from an explicit zero.
func envOptionalDuration(key string) (*time.Duration, error) {
	value := os.Getenv(key)
	if value == "" {
		return nil, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return nil, fmt.Errorf("%s must be a duration: %w", key, err)
	}
	return &parsed, nil
}

func envFloat(key string, fallback float64) (float64, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be a number: %w", key, err)
	}
	if math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return 0, fmt.Errorf("%s must be a finite number", key)
	}
	return parsed, nil
}
