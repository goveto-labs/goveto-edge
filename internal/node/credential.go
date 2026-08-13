package node

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

const credentialEnvelope = "enc:v2:"

type credentialKey struct {
	id   string
	aead cipher.AEAD
	key  []byte
}

type CredentialCipher struct {
	primary credentialKey
	keys    map[string]credentialKey
}

func NewCredentialCipher(encodedKey string) (*CredentialCipher, error) {
	return NewCredentialCipherKeyring(encodedKey)
}

// NewCredentialCipherKeyring creates a cipher that writes with encodedKey and
// can still decrypt values written by previousKeys. New ciphertexts carry a
// non-secret key ID so rotations do not require trial-decrypting every key.
func NewCredentialCipherKeyring(encodedKey string, previousKeys ...string) (*CredentialCipher, error) {
	primary, err := newCredentialKey(encodedKey)
	if err != nil {
		return nil, err
	}
	result := &CredentialCipher{primary: primary, keys: map[string]credentialKey{primary.id: primary}}
	for _, encodedPrevious := range previousKeys {
		if strings.TrimSpace(encodedPrevious) == "" {
			continue
		}
		previous, previousErr := newCredentialKey(encodedPrevious)
		if previousErr != nil {
			return nil, fmt.Errorf("previous credential key: %w", previousErr)
		}
		result.keys[previous.id] = previous
	}
	return result, nil
}

func newCredentialKey(encodedKey string) (credentialKey, error) {
	key, err := base64.StdEncoding.DecodeString(encodedKey)
	if err != nil {
		return credentialKey{}, err
	}
	if len(key) != 32 {
		return credentialKey{}, errors.New("credential master key must decode to 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return credentialKey{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return credentialKey{}, err
	}
	fingerprint := sha256.Sum256(key)
	return credentialKey{id: hex.EncodeToString(fingerprint[:8]), aead: aead, key: append([]byte(nil), key...)}, nil
}

// Derive returns a stable, domain-separated secret without exposing the
// credential encryption master key.
func (c *CredentialCipher) Derive(label string) []byte {
	mac := hmac.New(sha256.New, c.primary.key)
	_, _ = io.WriteString(mac, label)
	return mac.Sum(nil)
}

func (c *CredentialCipher) KeyID() string { return c.primary.id }

func (c *CredentialCipher) IsCurrent(value string) bool {
	return strings.HasPrefix(value, credentialEnvelope+c.primary.id+":")
}

// IsEnvelope reports whether value uses the versioned encrypted envelope.
// Callers migrating legacy plaintext can use this to fail closed for malformed
// ciphertext instead of accidentally treating it as plaintext.
func (c *CredentialCipher) IsEnvelope(value string) bool {
	return strings.HasPrefix(value, credentialEnvelope)
}

// Rewrap decrypts a value and writes it with the primary key when it was
// encrypted by a previous or legacy key. The boolean reports whether callers
// should persist the returned ciphertext.
func (c *CredentialCipher) Rewrap(value string) (string, bool, error) {
	return c.rewrap(value, nil)
}

func (c *CredentialCipher) RewrapScoped(scope, value string) (string, bool, error) {
	return c.rewrap(value, []byte(scope))
}

func (c *CredentialCipher) rewrap(value string, additionalData []byte) (string, bool, error) {
	if value == "" {
		return "", false, nil
	}
	if c.IsCurrent(value) {
		return value, false, nil
	}
	plain, err := c.decrypt(value, additionalData)
	if err != nil {
		return "", false, err
	}
	wrapped, err := c.encrypt(plain, additionalData)
	return wrapped, err == nil, err
}

func (c *CredentialCipher) Encrypt(value string) (string, error) {
	return c.encrypt(value, nil)
}

// EncryptScoped binds the ciphertext to a domain-specific context. A value
// encrypted for one cluster or credential cannot be decrypted in another.
func (c *CredentialCipher) EncryptScoped(scope, value string) (string, error) {
	return c.encrypt(value, []byte(scope))
}

func (c *CredentialCipher) encrypt(value string, additionalData []byte) (string, error) {
	nonce := make([]byte, c.primary.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := c.primary.aead.Seal(nonce, nonce, []byte(value), additionalData)
	return credentialEnvelope + c.primary.id + ":" + base64.RawStdEncoding.EncodeToString(sealed), nil
}

func (c *CredentialCipher) Decrypt(value string) (string, error) {
	return c.decrypt(value, nil)
}

// DecryptScoped decrypts a value only when the original encryption context
// matches scope.
func (c *CredentialCipher) DecryptScoped(scope, value string) (string, error) {
	return c.decrypt(value, []byte(scope))
}

func (c *CredentialCipher) decrypt(value string, additionalData []byte) (string, error) {
	if strings.HasPrefix(value, credentialEnvelope) {
		parts := strings.SplitN(strings.TrimPrefix(value, credentialEnvelope), ":", 2)
		if len(parts) != 2 {
			return "", errors.New("invalid encrypted credential envelope")
		}
		key, ok := c.keys[parts[0]]
		if !ok {
			return "", fmt.Errorf("credential key %s is unavailable", parts[0])
		}
		return decryptCredential(key, parts[1], additionalData)
	}
	var failures []error
	for _, key := range c.keys {
		plain, err := decryptCredential(key, value, additionalData)
		if err == nil {
			return plain, nil
		}
		failures = append(failures, err)
	}
	return "", fmt.Errorf("decrypt legacy credential with configured keys: %w", errors.Join(failures...))
}

func decryptCredential(key credentialKey, value string, additionalData []byte) (string, error) {
	data, err := base64.RawStdEncoding.DecodeString(value)
	if err != nil {
		return "", err
	}
	if len(data) < key.aead.NonceSize() {
		return "", errors.New("invalid encrypted credential")
	}
	plain, err := key.aead.Open(nil, data[:key.aead.NonceSize()], data[key.aead.NonceSize():], additionalData)
	return string(plain), err
}
