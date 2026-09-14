package configseal

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"goveto-edge/internal/edgeprotocol"
	"goveto-edge/internal/node"
)

func testCipher(t *testing.T) *node.CredentialCipher {
	t.Helper()
	key := sha256.Sum256([]byte("configseal-test"))
	cipher, err := node.NewCredentialCipher(base64.StdEncoding.EncodeToString(key[:]))
	if err != nil {
		t.Fatal(err)
	}
	return cipher
}

func secretBearingConfig() edgeprotocol.SiteConfig {
	config := edgeprotocol.SiteConfig{
		SiteID:  "site-1",
		Version: 7,
		Domains: []string{"example.test"},
		WAF:     map[string]any{"enabled": true, "challenge_secret": "waf-secret"},
	}
	config.Certificates = []edgeprotocol.CertificateConfig{
		{CertificatePEM: "certificate-pem", PrivateKeyPEM: "private-key-pem"},
	}
	config.OriginPolicy.Transport.TLSClientPrivateKeyPEM = "mtls-private-key"
	config.ACMEChallenges = []edgeprotocol.ACMEChallengeConfig{
		{Domain: "example.test", Token: "token", KeyAuth: "key-auth"},
	}
	return config
}

func TestSealUnsealRoundTrip(t *testing.T) {
	sealer := New(testCipher(t))
	config := secretBearingConfig()

	if sealer.Sealed(&config) {
		t.Fatal("fresh config reported as sealed")
	}
	if !sealer.HasSecrets(&config) {
		t.Fatal("fresh config reported as secret-free")
	}
	if err := sealer.SealSiteConfigSecrets("site-1", 7, &config); err != nil {
		t.Fatal(err)
	}
	if !sealer.Sealed(&config) {
		t.Fatal("sealed config not detected")
	}

	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-key-pem", "mtls-private-key", "waf-secret", "key-auth"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("secret %q survives in sealed snapshot: %s", secret, encoded)
		}
	}
	for _, visible := range []string{"certificate-pem", "token", "example.test"} {
		if !strings.Contains(string(encoded), visible) {
			t.Fatalf("public material %q missing from sealed snapshot: %s", visible, encoded)
		}
	}

	var stored edgeprotocol.SiteConfig
	if err = json.Unmarshal(encoded, &stored); err != nil {
		t.Fatal(err)
	}
	if err = sealer.UnsealSiteConfigSecrets("site-1", 7, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Certificates[0].PrivateKeyPEM != "private-key-pem" ||
		stored.OriginPolicy.Transport.TLSClientPrivateKeyPEM != "mtls-private-key" ||
		stored.ACMEChallenges[0].KeyAuth != "key-auth" {
		t.Fatalf("unsealed secrets mismatch: %#v", stored)
	}
	if secret, _ := stored.WAF["challenge_secret"].(string); secret != "waf-secret" {
		t.Fatalf("WAF challenge secret mismatch: %#v", stored.WAF)
	}
}

func TestUnsealRejectsForeignScope(t *testing.T) {
	sealer := New(testCipher(t))
	config := secretBearingConfig()
	if err := sealer.SealSiteConfigSecrets("site-1", 7, &config); err != nil {
		t.Fatal(err)
	}
	for _, scope := range [][2]any{{"site-2", uint64(7)}, {"site-1", uint64(8)}} {
		copied := config
		if err := sealer.UnsealSiteConfigSecrets(scope[0].(string), scope[1].(uint64), &copied); err == nil {
			t.Fatalf("unseal with scope %v accepted foreign ciphertext", scope)
		}
	}
}

func TestSealRejectsAlreadySealedValues(t *testing.T) {
	sealer := New(testCipher(t))
	config := secretBearingConfig()
	if err := sealer.SealSiteConfigSecrets("site-1", 7, &config); err != nil {
		t.Fatal(err)
	}
	if err := sealer.SealSiteConfigSecrets("site-1", 8, &config); err == nil {
		t.Fatal("double sealing was accepted")
	}
}

func TestUnsealPassesLegacyPlaintextThrough(t *testing.T) {
	sealer := New(testCipher(t))
	config := secretBearingConfig()
	if sealer.Sealed(&config) {
		t.Fatal("plaintext config reported as sealed")
	}
	if err := sealer.UnsealSiteConfigSecrets("site-1", 7, &config); err != nil {
		t.Fatalf("legacy plaintext rejected: %v", err)
	}
	if config.Certificates[0].PrivateKeyPEM != "private-key-pem" {
		t.Fatal("legacy plaintext mutated")
	}
}

func TestEmptyConfigIsNoOp(t *testing.T) {
	sealer := New(testCipher(t))
	config := edgeprotocol.SiteConfig{SiteID: "site-1", Version: 1}
	if sealer.HasSecrets(&config) || sealer.Sealed(&config) {
		t.Fatal("empty config misclassified")
	}
	if err := sealer.SealSiteConfigSecrets("site-1", 1, &config); err != nil {
		t.Fatal(err)
	}
	if err := sealer.UnsealSiteConfigSecrets("site-1", 1, &config); err != nil {
		t.Fatal(err)
	}
}

func mtlsPolicy() edgeprotocol.OriginPolicyConfig {
	policy := edgeprotocol.DefaultOriginPolicy()
	policy.Transport.TLSClientCertificatePEM = "mtls-certificate"
	policy.Transport.TLSClientPrivateKeyPEM = "mtls-private-key"
	return policy
}

func TestOriginPolicySealUnsealRoundTrip(t *testing.T) {
	sealer := New(testCipher(t))
	policy := mtlsPolicy()
	if sealer.SealedOriginPolicy(&policy) {
		t.Fatal("plaintext policy reported as sealed")
	}
	if !sealer.HasOriginPolicySecrets(&policy) {
		t.Fatal("mtls policy reported as secret-free")
	}
	if err := sealer.SealOriginPolicySecrets("cluster-1", "pool-1", &policy); err != nil {
		t.Fatal(err)
	}
	if !sealer.SealedOriginPolicy(&policy) {
		t.Fatal("sealed policy not detected")
	}
	if policy.Transport.TLSClientCertificatePEM != "mtls-certificate" {
		t.Fatal("public certificate PEM was sealed")
	}
	if strings.Contains(policy.Transport.TLSClientPrivateKeyPEM, "mtls-private-key") {
		t.Fatal("mtls key survives sealing")
	}
	if err := sealer.UnsealOriginPolicySecrets("cluster-1", "pool-1", &policy); err != nil {
		t.Fatal(err)
	}
	if policy.Transport.TLSClientPrivateKeyPEM != "mtls-private-key" {
		t.Fatal("mtls key did not round-trip")
	}
}

func TestOriginPolicyForeignScopeRejected(t *testing.T) {
	sealer := New(testCipher(t))
	policy := mtlsPolicy()
	if err := sealer.SealOriginPolicySecrets("cluster-1", "pool-1", &policy); err != nil {
		t.Fatal(err)
	}
	for _, sc := range [][2]string{{"cluster-2", "pool-1"}, {"cluster-1", "pool-2"}} {
		copied := policy
		if err := sealer.UnsealOriginPolicySecrets(sc[0], sc[1], &copied); err == nil {
			t.Fatalf("unseal accepted foreign scope %v", sc)
		}
	}
}

func TestOriginPolicyRewrapSealsLegacyPlaintext(t *testing.T) {
	key := sha256.Sum256([]byte("configseal-test"))
	primary, err := node.NewCredentialCipher(base64.StdEncoding.EncodeToString(key[:]))
	if err != nil {
		t.Fatal(err)
	}
	rotatedKey := sha256.Sum256([]byte("configseal-rotated"))
	next, err := node.NewCredentialCipherKeyring(
		base64.StdEncoding.EncodeToString(rotatedKey[:]),
		base64.StdEncoding.EncodeToString(key[:]),
	)
	if err != nil {
		t.Fatal(err)
	}

	// Legacy plaintext is sealed onto the primary key.
	sealer := New(primary)
	policy := mtlsPolicy()
	changed, err := sealer.RewrapOriginPolicySecrets("cluster-1", "pool-1", &policy)
	if err != nil || !changed || !sealer.SealedOriginPolicy(&policy) {
		t.Fatalf("legacy rewrap changed=%v err=%v", changed, err)
	}

	// Ciphertext from a previous key is readable by the rotated keyring and
	// rewritten onto the new primary; already-current rows report no change.
	rotated := New(next)
	changed, err = rotated.RewrapOriginPolicySecrets("cluster-1", "pool-1", &policy)
	if err != nil || !changed {
		t.Fatalf("rotation rewrap changed=%v err=%v", changed, err)
	}
	if changed, err = rotated.RewrapOriginPolicySecrets("cluster-1", "pool-1", &policy); err != nil || changed {
		t.Fatalf("idempotent rewrap changed=%v err=%v", changed, err)
	}
	if err = rotated.UnsealOriginPolicySecrets("cluster-1", "pool-1", &policy); err != nil {
		t.Fatal(err)
	}
	if policy.Transport.TLSClientPrivateKeyPEM != "mtls-private-key" {
		t.Fatal("rotated mtls key did not round-trip")
	}
}

func TestOriginPolicyWithoutSecretsIsNoOp(t *testing.T) {
	sealer := New(testCipher(t))
	policy := edgeprotocol.DefaultOriginPolicy()
	if sealer.HasOriginPolicySecrets(&policy) || sealer.SealedOriginPolicy(&policy) {
		t.Fatal("secret-free policy misclassified")
	}
	if err := sealer.SealOriginPolicySecrets("cluster-1", "pool-1", &policy); err != nil {
		t.Fatal(err)
	}
	if err := sealer.UnsealOriginPolicySecrets("cluster-1", "pool-1", &policy); err != nil {
		t.Fatal(err)
	}
	changed, err := sealer.RewrapOriginPolicySecrets("cluster-1", "pool-1", &policy)
	if err != nil || changed {
		t.Fatalf("secret-free rewrap changed=%v err=%v", changed, err)
	}
}

func TestRewrapMigratesPreviousKeyAndLegacyPlaintext(t *testing.T) {
	previousKey := base64.StdEncoding.EncodeToString(sha256Sum("configseal-previous"))
	currentKey := base64.StdEncoding.EncodeToString(sha256Sum("configseal-current"))
	previousCipher, err := node.NewCredentialCipher(previousKey)
	if err != nil {
		t.Fatal(err)
	}
	currentCipher, err := node.NewCredentialCipherKeyring(currentKey, previousKey)
	if err != nil {
		t.Fatal(err)
	}
	previous := New(previousCipher)
	current := New(currentCipher)

	sealed := secretBearingConfig()
	if err = previous.SealSiteConfigSecrets("site-1", 7, &sealed); err != nil {
		t.Fatal(err)
	}
	changed, err := current.RewrapSiteConfigSecrets("site-1", 7, &sealed)
	if err != nil || !changed {
		t.Fatalf("previous-key rewrap changed=%v err=%v", changed, err)
	}
	onlyCurrent, err := node.NewCredentialCipher(currentKey)
	if err != nil {
		t.Fatal(err)
	}
	if err = New(onlyCurrent).UnsealSiteConfigSecrets("site-1", 7, &sealed); err != nil {
		t.Fatalf("rewrapped snapshot still requires previous key: %v", err)
	}
	if sealed.Certificates[0].PrivateKeyPEM != "private-key-pem" {
		t.Fatal("rewrapped snapshot did not round-trip")
	}

	legacy := secretBearingConfig()
	changed, err = current.RewrapSiteConfigSecrets("site-1", 7, &legacy)
	if err != nil || !changed || !current.Sealed(&legacy) {
		t.Fatalf("legacy plaintext was not sealed: changed=%v err=%v", changed, err)
	}

	currentOnly := secretBearingConfig()
	if err = New(onlyCurrent).SealSiteConfigSecrets("site-1", 7, &currentOnly); err != nil {
		t.Fatal(err)
	}
	changed, err = New(onlyCurrent).RewrapSiteConfigSecrets("site-1", 7, &currentOnly)
	if err != nil || changed {
		t.Fatalf("current-key rewrap changed=%v err=%v", changed, err)
	}
}

func sha256Sum(value string) []byte {
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}
