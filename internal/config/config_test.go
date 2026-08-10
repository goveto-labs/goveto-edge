package config

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestLoadUsesSharedMasterKeyWithoutLocalSecretFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not-created")
	t.Setenv("DATABASE_URL", "postgresql://localhost/goveto")
	t.Setenv("REDIS_URL", "redis://localhost:6379/0")
	t.Setenv("GOVETO_DATA_DIR", dir)
	shared := base64.StdEncoding.EncodeToString(make([]byte, 32))
	t.Setenv("NODE_CREDENTIAL_MASTER_KEY", shared)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NodeCredentialMasterKey != shared {
		t.Fatal("shared master key was not used")
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("shared master key unexpectedly created local data: %v", err)
	}
}

func TestLoadSeparatesPurposeKeysAndAcceptsFileProvider(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DATABASE_URL", "postgresql://localhost/goveto")
	t.Setenv("REDIS_URL", "redis://localhost:6379/0")
	t.Setenv("GOVETO_DATA_DIR", dir)
	root := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	fileKey := base64.StdEncoding.EncodeToString([]byte("abcdef0123456789abcdef0123456789"))
	keyFile := filepath.Join(dir, "certificate.key")
	if err := os.WriteFile(keyFile, []byte(fileKey+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NODE_CREDENTIAL_MASTER_KEY", root)
	t.Setenv("CERTIFICATE_MASTER_KEY_FILE", keyFile)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CertificateMasterKey != fileKey {
		t.Fatal("certificate key file was not used")
	}
	if cfg.DNSCredentialMasterKey == root || cfg.NotificationMasterKey == root || cfg.AgentCAMasterKey == root {
		t.Fatal("purpose keys were not domain separated")
	}
}

func TestLoadFromEnvironment(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgresql://localhost/goveto")
	t.Setenv("REDIS_URL", "redis://localhost:6379/0")
	t.Setenv("GOVETO_DATA_DIR", t.TempDir())
	t.Setenv("HTTP_PORT", "9090")
	t.Setenv("AGENT_GATEWAY_PORT", "9443")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddress() != "0.0.0.0:9090" {
		t.Fatalf("unexpected HTTP address: %s", cfg.HTTPAddress())
	}
	if cfg.AgentGatewayAddress() != "0.0.0.0:9443" {
		t.Fatalf("unexpected agent gateway address: %s", cfg.AgentGatewayAddress())
	}
}

func TestAnalyticsDatabaseDefaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgresql://localhost/goveto")
	t.Setenv("REDIS_URL", "redis://localhost:6379/0")
	t.Setenv("GOVETO_DATA_DIR", t.TempDir())
	t.Setenv("NODE_CREDENTIAL_MASTER_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("ANALYTICS_DATABASE_URL", "")
	t.Setenv("ANALYTICS_DB_MAX_CONNS", "")
	t.Setenv("ANALYTICS_QUERY_TIMEOUT", "")
	t.Setenv("CLICKHOUSE_DSN", "clickhouse://ignored")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AnalyticsDatabaseURL != cfg.DatabaseURL || cfg.AnalyticsDBMaxConns != 16 || cfg.AnalyticsQueryTimeout != 5*time.Second {
		t.Fatalf("unexpected analytics defaults: %#v", cfg)
	}
}

func TestAnalyticsDatabaseOverrides(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgresql://localhost/control")
	t.Setenv("REDIS_URL", "redis://localhost:6379/0")
	t.Setenv("GOVETO_DATA_DIR", t.TempDir())
	t.Setenv("NODE_CREDENTIAL_MASTER_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("ANALYTICS_DATABASE_URL", "postgresql://localhost/analytics")
	t.Setenv("ANALYTICS_DB_MAX_CONNS", "32")
	t.Setenv("ANALYTICS_QUERY_TIMEOUT", "2500ms")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AnalyticsDatabaseURL != "postgresql://localhost/analytics" || cfg.AnalyticsDBMaxConns != 32 || cfg.AnalyticsQueryTimeout != 2500*time.Millisecond {
		t.Fatalf("unexpected analytics overrides: %#v", cfg)
	}
}

func TestAnalyticsDatabaseRejectsInvalidLimits(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgresql://localhost/goveto")
	t.Setenv("REDIS_URL", "redis://localhost:6379/0")
	t.Setenv("GOVETO_DATA_DIR", t.TempDir())
	t.Setenv("NODE_CREDENTIAL_MASTER_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("ANALYTICS_DB_MAX_CONNS", "0")
	if _, err := Load(); err == nil {
		t.Fatal("zero analytics connection limit was accepted")
	}
	t.Setenv("ANALYTICS_DB_MAX_CONNS", "16")
	t.Setenv("ANALYTICS_QUERY_TIMEOUT", "0s")
	if _, err := Load(); err == nil {
		t.Fatal("zero analytics query timeout was accepted")
	}
}

func TestGeoIPConfigurationDefaultsAndProductionOptIn(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgresql://localhost/goveto")
	t.Setenv("REDIS_URL", "redis://localhost:6379/0")
	t.Setenv("GOVETO_DATA_DIR", t.TempDir())
	t.Setenv("NODE_CREDENTIAL_MASTER_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("GEOIP_DATABASE_PATH", "")
	t.Setenv("GEOIP_ASN_DATABASE_PATH", "")
	t.Setenv("GEOIP_DATABASE_POLL_INTERVAL", "")
	t.Setenv("APP_ENV", "test")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GeoIPDatabasePath != "./GeoLite2-City.mmdb" ||
		cfg.GeoIPASNDatabasePath != "./GeoLite2-ASN.mmdb" ||
		cfg.GeoIPDatabasePollInterval != 30*time.Second {
		t.Fatalf("unexpected test GeoIP defaults: %#v", cfg)
	}
	t.Setenv("APP_ENV", "production")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GeoIPDatabasePath != "" || cfg.GeoIPASNDatabasePath != "" {
		t.Fatalf("production GeoIP should require opt-in, got city=%q ASN=%q", cfg.GeoIPDatabasePath, cfg.GeoIPASNDatabasePath)
	}
}

func TestLoadS3ArchiveConfiguration(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgresql://localhost/goveto")
	t.Setenv("REDIS_URL", "redis://localhost:6379/0")
	t.Setenv("GOVETO_DATA_DIR", t.TempDir())
	t.Setenv("ANALYTICS_ARCHIVE_DIR", "")
	t.Setenv("ANALYTICS_ARCHIVE_S3_ENDPOINT", "https://objects.example.com")
	t.Setenv("ANALYTICS_ARCHIVE_S3_BUCKET", "edge-logs")
	t.Setenv("ANALYTICS_ARCHIVE_S3_REGION", "us-east-1")
	t.Setenv("ANALYTICS_ARCHIVE_S3_ACCESS_KEY", "access")
	t.Setenv("ANALYTICS_ARCHIVE_S3_SECRET_KEY", "secret")
	t.Setenv("ANALYTICS_ARCHIVE_S3_SESSION_TOKEN", "token")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AnalyticsArchiveS3Endpoint != "https://objects.example.com" ||
		cfg.AnalyticsArchiveS3Bucket != "edge-logs" || cfg.AnalyticsArchiveS3SessionToken != "token" {
		t.Fatalf("unexpected S3 archive configuration: %#v", cfg)
	}
}

func TestLoadRejectsIncompleteS3ArchiveConfiguration(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgresql://localhost/goveto")
	t.Setenv("REDIS_URL", "redis://localhost:6379/0")
	t.Setenv("GOVETO_DATA_DIR", t.TempDir())
	t.Setenv("ANALYTICS_ARCHIVE_DIR", "")
	t.Setenv("ANALYTICS_ARCHIVE_S3_ENDPOINT", "https://objects.example.com")
	t.Setenv("ANALYTICS_ARCHIVE_S3_BUCKET", "")
	t.Setenv("ANALYTICS_ARCHIVE_S3_REGION", "us-east-1")
	t.Setenv("ANALYTICS_ARCHIVE_S3_ACCESS_KEY", "access")
	t.Setenv("ANALYTICS_ARCHIVE_S3_SECRET_KEY", "secret")

	if _, err := Load(); err == nil {
		t.Fatal("incomplete S3 archive configuration was accepted")
	}
}

func TestLoadRejectsMultipleArchiveStores(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgresql://localhost/goveto")
	t.Setenv("REDIS_URL", "redis://localhost:6379/0")
	t.Setenv("GOVETO_DATA_DIR", t.TempDir())
	t.Setenv("ANALYTICS_ARCHIVE_DIR", "/tmp/archive")
	t.Setenv("ANALYTICS_ARCHIVE_S3_ENDPOINT", "https://objects.example.com")
	t.Setenv("ANALYTICS_ARCHIVE_S3_BUCKET", "edge-logs")
	t.Setenv("ANALYTICS_ARCHIVE_S3_REGION", "us-east-1")
	t.Setenv("ANALYTICS_ARCHIVE_S3_ACCESS_KEY", "access")
	t.Setenv("ANALYTICS_ARCHIVE_S3_SECRET_KEY", "secret")

	if _, err := Load(); err == nil {
		t.Fatal("filesystem and S3 archives were both accepted")
	}
}

func TestProductionForcesSecureSessionCookie(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgresql://localhost/goveto")
	t.Setenv("REDIS_URL", "redis://localhost:6379/0")
	t.Setenv("GOVETO_DATA_DIR", t.TempDir())
	t.Setenv("APP_ENV", "production")
	t.Setenv("SESSION_COOKIE_SECURE", "false")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.SessionCookieSecure {
		t.Fatal("production accepted an insecure session cookie")
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

func TestPurposeMasterKeyMigratesAfterRootKeyRotation(t *testing.T) {
	dir := t.TempDir()
	rootA := base64.StdEncoding.EncodeToString([]byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"))
	rootB := base64.StdEncoding.EncodeToString([]byte("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"))
	derivedA := deriveEncodedMasterKey(rootA, "test/purpose/v1")
	derivedB := deriveEncodedMasterKey(rootB, "test/purpose/v1")

	first, err := loadOrMigrateNamedMasterKey(dir, "purpose.key", derivedA, keyFingerprint(rootA))
	if err != nil {
		t.Fatalf("first boot: %v", err)
	}
	if first != derivedA {
		t.Fatalf("first boot did not persist the derived value")
	}

	// Same root on reboot: stable, no rewrite.
	again, err := loadOrMigrateNamedMasterKey(dir, "purpose.key", derivedA, keyFingerprint(rootA))
	if err != nil {
		t.Fatalf("reboot: %v", err)
	}
	if again != derivedA {
		t.Fatal("purpose key changed without a root key rotation")
	}

	// Root key rotates: persisted purpose key must be re-derived and overwritten.
	rotated, err := loadOrMigrateNamedMasterKey(dir, "purpose.key", derivedB, keyFingerprint(rootB))
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if rotated != derivedB {
		t.Fatalf("rotated key was not migrated: got %s want %s", rotated, derivedB)
	}

	// Companion now records the new root; reboot stays on the new value.
	stable, err := loadOrMigrateNamedMasterKey(dir, "purpose.key", derivedB, keyFingerprint(rootB))
	if err != nil {
		t.Fatalf("stable after rotate: %v", err)
	}
	if stable != derivedB {
		t.Fatal("migrated purpose key was not stable after rotation")
	}
}

func TestPurposeMasterKeyBackfillsLegacyCompanionFile(t *testing.T) {
	dir := t.TempDir()
	secretsDir := filepath.Join(dir, "secrets")
	if err := os.MkdirAll(secretsDir, 0700); err != nil {
		t.Fatal(err)
	}
	root := base64.StdEncoding.EncodeToString([]byte("cccccccccccccccccccccccccccccccc"))
	derived := deriveEncodedMasterKey(root, "test/purpose/v1")
	// Simulate a legacy install: purpose key file written by older code, no companion.
	if err := os.WriteFile(filepath.Join(secretsDir, "purpose.key"), []byte(derived+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := loadOrMigrateNamedMasterKey(dir, "purpose.key", derived, keyFingerprint(root))
	if err != nil {
		t.Fatalf("legacy backfill: %v", err)
	}
	if got != derived {
		t.Fatal("legacy purpose key should be preserved when it matches the current root")
	}
	if _, err := os.Stat(filepath.Join(secretsDir, "purpose.key.source")); err != nil {
		t.Fatalf("companion source file was not backfilled: %v", err)
	}
}
