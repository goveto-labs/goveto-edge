// Package configseal seals sensitive fields of site configuration snapshots
// before the control plane persists them (config version rows and agent task
// payloads). Sealing reuses the node credential keyring: every value is
// encrypted with AES-256-GCM under an additional-data scope that binds the
// ciphertext to its purpose, site, and version, so a sealed value cannot be
// replayed against another site or version row.
//
// Edge nodes have no access to the master key and expect plaintext
// credentials. Persisted rows stay sealed; the agent gateway unseals
// immediately before the mTLS write. Rows written before sealing existed
// still carry plaintext; unsealing passes those through unchanged so the
// migration window cannot break publishing.
package configseal

import (
	"errors"
	"fmt"
	"strconv"

	"goveto-edge/internal/edgeprotocol"
	"goveto-edge/internal/node"
)

const scopePrefix = "goveto-edge/site-config-secret/v1"

const originScopePrefix = "goveto-edge/origin-governance-secret/v1"

// Field tags bound into the encryption scope. They double as the inventory of
// SiteConfig fields that are considered secret at rest.
const (
	fieldCertificatePrivateKey = "certificates.private_key"
	fieldOriginMTLSPrivateKey  = "origin_policy.transport.tls_client_private_key_pem"
	fieldWAFChallengeSecret    = "waf.challenge_secret"
	fieldACMEKeyAuthorization  = "acme_challenges.key_auth"
	fieldOriginPoolMTLSKey     = "transport.tls_client_private_key_pem"
)

func originScope(clusterID, poolID string) string {
	return originScopePrefix + "\x00" + clusterID + "\x00" + poolID + "\x00" + fieldOriginPoolMTLSKey
}

// ErrSealerUnavailable is returned when sealing is required but no cipher
// was configured. Production wiring must fail closed rather than persist
// plaintext secrets.
var ErrSealerUnavailable = errors.New("config seal cipher is not configured")

// Sealer seals and unseals sensitive SiteConfig fields. It is safe for
// concurrent use; the underlying cipher is immutable after construction.
type Sealer struct {
	cipher *node.CredentialCipher
}

// New wraps a credential cipher for site-config sealing.
func New(cipher *node.CredentialCipher) *Sealer {
	return &Sealer{cipher: cipher}
}

func scope(siteID string, version uint64, field string) string {
	return scopePrefix + "\x00" + siteID + "\x00" + strconv.FormatUint(version, 10) + "\x00" + field
}

// SealSiteConfigSecrets encrypts every sensitive field in place, scoped to the
// given site and version. It fails closed when a value already carries an
// envelope: double sealing hides programming errors and yields ciphertext
// that can never be unsealed again.
func (s *Sealer) SealSiteConfigSecrets(siteID string, version uint64, config *edgeprotocol.SiteConfig) error {
	if s == nil || s.cipher == nil {
		return ErrSealerUnavailable
	}
	for index := range config.Certificates {
		scoped := scope(siteID, version, fieldCertificatePrivateKey)
		if err := s.seal(scoped, &config.Certificates[index].PrivateKeyPEM); err != nil {
			return fmt.Errorf("certificate %d: %w", index, err)
		}
	}
	if err := s.seal(scope(siteID, version, fieldOriginMTLSPrivateKey), &config.OriginPolicy.Transport.TLSClientPrivateKeyPEM); err != nil {
		return fmt.Errorf("origin mTLS: %w", err)
	}
	if err := s.sealWAFChallengeSecret(scope(siteID, version, fieldWAFChallengeSecret), config.WAF); err != nil {
		return fmt.Errorf("WAF challenge secret: %w", err)
	}
	for index := range config.ACMEChallenges {
		scoped := scope(siteID, version, fieldACMEKeyAuthorization)
		if err := s.seal(scoped, &config.ACMEChallenges[index].KeyAuth); err != nil {
			return fmt.Errorf("ACME challenge %d: %w", index, err)
		}
	}
	return nil
}

// UnsealSiteConfigSecrets decrypts sensitive fields in place using the scope
// they were sealed with. Plaintext values (legacy rows predating sealing, or
// in-memory configs) pass through unchanged.
func (s *Sealer) UnsealSiteConfigSecrets(siteID string, version uint64, config *edgeprotocol.SiteConfig) error {
	if s == nil || s.cipher == nil {
		return ErrSealerUnavailable
	}
	for index := range config.Certificates {
		scoped := scope(siteID, version, fieldCertificatePrivateKey)
		if err := s.unseal(scoped, &config.Certificates[index].PrivateKeyPEM); err != nil {
			return fmt.Errorf("certificate %d: %w", index, err)
		}
	}
	if err := s.unseal(scope(siteID, version, fieldOriginMTLSPrivateKey), &config.OriginPolicy.Transport.TLSClientPrivateKeyPEM); err != nil {
		return fmt.Errorf("origin mTLS: %w", err)
	}
	if err := s.unsealWAFChallengeSecret(scope(siteID, version, fieldWAFChallengeSecret), config.WAF); err != nil {
		return fmt.Errorf("WAF challenge secret: %w", err)
	}
	for index := range config.ACMEChallenges {
		scoped := scope(siteID, version, fieldACMEKeyAuthorization)
		if err := s.unseal(scoped, &config.ACMEChallenges[index].KeyAuth); err != nil {
			return fmt.Errorf("ACME challenge %d: %w", index, err)
		}
	}
	return nil
}

// Sealed reports whether any sensitive field already carries an envelope.
// Callers use it to detect rows that still need the sealing migration.
func (s *Sealer) Sealed(config *edgeprotocol.SiteConfig) bool {
	if s == nil || s.cipher == nil {
		return false
	}
	for index := range config.Certificates {
		if s.cipher.IsEnvelope(config.Certificates[index].PrivateKeyPEM) {
			return true
		}
	}
	if s.cipher.IsEnvelope(config.OriginPolicy.Transport.TLSClientPrivateKeyPEM) {
		return true
	}
	if value, ok := config.WAF["challenge_secret"]; ok {
		if secret, ok := value.(string); ok && s.cipher.IsEnvelope(secret) {
			return true
		}
	}
	for index := range config.ACMEChallenges {
		if s.cipher.IsEnvelope(config.ACMEChallenges[index].KeyAuth) {
			return true
		}
	}
	return false
}

// HasSecrets reports whether any sensitive field is set. Combined with
// Sealed it classifies rows for the backfill migration: HasSecrets && !Sealed
// means a legacy row that still persists plaintext.
func (s *Sealer) HasSecrets(config *edgeprotocol.SiteConfig) bool {
	for index := range config.Certificates {
		if config.Certificates[index].PrivateKeyPEM != "" {
			return true
		}
	}
	if config.OriginPolicy.Transport.TLSClientPrivateKeyPEM != "" {
		return true
	}
	if value, ok := config.WAF["challenge_secret"]; ok {
		if secret, ok := value.(string); ok && secret != "" {
			return true
		}
	}
	for index := range config.ACMEChallenges {
		if config.ACMEChallenges[index].KeyAuth != "" {
			return true
		}
	}
	return false
}

// RewrapSiteConfigSecrets re-encrypts sealed fields with the current primary
// key and encrypts leftover plaintext secrets. changed reports whether the
// caller should persist the mutated config; without that write, dropping
// previous keys would make historical snapshots unreadable.
func (s *Sealer) RewrapSiteConfigSecrets(siteID string, version uint64, config *edgeprotocol.SiteConfig) (bool, error) {
	if s == nil || s.cipher == nil {
		return false, ErrSealerUnavailable
	}
	changed := false
	for index := range config.Certificates {
		rewrapped, err := s.rewrap(scope(siteID, version, fieldCertificatePrivateKey), &config.Certificates[index].PrivateKeyPEM)
		if err != nil {
			return false, fmt.Errorf("certificate %d: %w", index, err)
		}
		changed = changed || rewrapped
	}
	rewrapped, err := s.rewrap(scope(siteID, version, fieldOriginMTLSPrivateKey), &config.OriginPolicy.Transport.TLSClientPrivateKeyPEM)
	if err != nil {
		return false, fmt.Errorf("origin mTLS: %w", err)
	}
	changed = changed || rewrapped
	rewrapped, err = s.rewrapWAFChallengeSecret(scope(siteID, version, fieldWAFChallengeSecret), config.WAF)
	if err != nil {
		return false, fmt.Errorf("WAF challenge secret: %w", err)
	}
	changed = changed || rewrapped
	for index := range config.ACMEChallenges {
		rewrapped, err = s.rewrap(scope(siteID, version, fieldACMEKeyAuthorization), &config.ACMEChallenges[index].KeyAuth)
		if err != nil {
			return false, fmt.Errorf("ACME challenge %d: %w", index, err)
		}
		changed = changed || rewrapped
	}
	return changed, nil
}

// SealOriginPolicySecrets encrypts the origin mTLS client key inside a
// governance policy in place, scoped to its cluster and origin pool. The
// certificate PEM stays public. Callers must validate the policy first:
// validation cannot reason about envelope strings.
func (s *Sealer) SealOriginPolicySecrets(clusterID, poolID string, policy *edgeprotocol.OriginPolicyConfig) error {
	if s == nil || s.cipher == nil {
		return ErrSealerUnavailable
	}
	scoped := originScope(clusterID, poolID)
	if err := s.seal(scoped, &policy.Transport.TLSClientPrivateKeyPEM); err != nil {
		return fmt.Errorf("origin mTLS: %w", err)
	}
	return nil
}

// UnsealOriginPolicySecrets decrypts the origin mTLS client key in place.
// Governance rows written before sealing carry plaintext and pass through
// unchanged.
func (s *Sealer) UnsealOriginPolicySecrets(clusterID, poolID string, policy *edgeprotocol.OriginPolicyConfig) error {
	if s == nil || s.cipher == nil {
		return ErrSealerUnavailable
	}
	scoped := originScope(clusterID, poolID)
	if err := s.unseal(scoped, &policy.Transport.TLSClientPrivateKeyPEM); err != nil {
		return fmt.Errorf("origin mTLS: %w", err)
	}
	return nil
}

// SealedOriginPolicy reports whether the governance policy carries a sealed
// mTLS key. It never fails on plaintext rows.
func (s *Sealer) SealedOriginPolicy(policy *edgeprotocol.OriginPolicyConfig) bool {
	if s == nil || s.cipher == nil {
		return false
	}
	return s.cipher.IsEnvelope(policy.Transport.TLSClientPrivateKeyPEM)
}

// HasOriginPolicySecrets reports whether the policy carries an mTLS client
// key at all. It is safe on a nil sealer: callers use it to fail closed when
// secrets must be persisted but no cipher is configured.
func (s *Sealer) HasOriginPolicySecrets(policy *edgeprotocol.OriginPolicyConfig) bool {
	return policy.Transport.TLSClientPrivateKeyPEM != ""
}

// RewrapOriginPolicySecrets reseals the mTLS key onto the current primary key
// and encrypts leftover plaintext. changed reports whether callers should
// persist the mutated policy.
func (s *Sealer) RewrapOriginPolicySecrets(clusterID, poolID string, policy *edgeprotocol.OriginPolicyConfig) (bool, error) {
	if s == nil || s.cipher == nil {
		return false, ErrSealerUnavailable
	}
	rewrapped, err := s.rewrap(originScope(clusterID, poolID), &policy.Transport.TLSClientPrivateKeyPEM)
	if err != nil {
		return false, fmt.Errorf("origin mTLS: %w", err)
	}
	return rewrapped, nil
}

func (s *Sealer) seal(scoped string, value *string) error {
	if *value == "" {
		return nil
	}
	if s.cipher.IsEnvelope(*value) {
		return errors.New("value is already sealed")
	}
	wrapped, err := s.cipher.EncryptScoped(scoped, *value)
	if err != nil {
		return err
	}
	*value = wrapped
	return nil
}

func (s *Sealer) unseal(scoped string, value *string) error {
	if *value == "" || !s.cipher.IsEnvelope(*value) {
		return nil
	}
	plain, err := s.cipher.DecryptScoped(scoped, *value)
	if err != nil {
		return err
	}
	*value = plain
	return nil
}

func (s *Sealer) rewrap(scoped string, value *string) (bool, error) {
	if *value == "" {
		return false, nil
	}
	if s.cipher.IsEnvelope(*value) {
		wrapped, changed, err := s.cipher.RewrapScoped(scoped, *value)
		if err != nil {
			return false, err
		}
		if changed {
			*value = wrapped
		}
		return changed, nil
	}
	if err := s.seal(scoped, value); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Sealer) sealWAFChallengeSecret(scoped string, waf map[string]any) error {
	value, ok := waf["challenge_secret"]
	if !ok {
		return nil
	}
	secret, ok := value.(string)
	if !ok {
		return fmt.Errorf("unexpected %T value", value)
	}
	holder := secret
	if err := s.seal(scoped, &holder); err != nil {
		return err
	}
	if holder != secret {
		waf["challenge_secret"] = holder
	}
	return nil
}

func (s *Sealer) unsealWAFChallengeSecret(scoped string, waf map[string]any) error {
	value, ok := waf["challenge_secret"]
	if !ok {
		return nil
	}
	secret, ok := value.(string)
	if !ok || secret == "" || !s.cipher.IsEnvelope(secret) {
		return nil
	}
	holder := secret
	if err := s.unseal(scoped, &holder); err != nil {
		return err
	}
	waf["challenge_secret"] = holder
	return nil
}

func (s *Sealer) rewrapWAFChallengeSecret(scoped string, waf map[string]any) (bool, error) {
	value, ok := waf["challenge_secret"]
	if !ok {
		return false, nil
	}
	secret, ok := value.(string)
	if !ok {
		return false, fmt.Errorf("unexpected %T value", value)
	}
	holder := secret
	changed, err := s.rewrap(scoped, &holder)
	if err != nil {
		return false, err
	}
	if changed {
		waf["challenge_secret"] = holder
	}
	return changed, nil
}
