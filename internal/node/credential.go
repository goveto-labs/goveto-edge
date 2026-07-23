package node

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
)

type CredentialCipher struct {
	aead cipher.AEAD
	key  []byte
}

func NewCredentialCipher(encodedKey string) (*CredentialCipher, error) {
	key, err := base64.StdEncoding.DecodeString(encodedKey)
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, errors.New("node credential master key must decode to 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &CredentialCipher{aead: aead, key: append([]byte(nil), key...)}, nil
}

// Derive returns a stable, domain-separated secret without exposing the
// credential encryption master key.
func (c *CredentialCipher) Derive(label string) []byte {
	mac := hmac.New(sha256.New, c.key)
	_, _ = io.WriteString(mac, label)
	return mac.Sum(nil)
}

func (c *CredentialCipher) Encrypt(value string) (string, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := c.aead.Seal(nonce, nonce, []byte(value), nil)
	return base64.RawStdEncoding.EncodeToString(sealed), nil
}
func (c *CredentialCipher) Decrypt(value string) (string, error) {
	data, err := base64.RawStdEncoding.DecodeString(value)
	if err != nil {
		return "", err
	}
	if len(data) < c.aead.NonceSize() {
		return "", errors.New("invalid encrypted node credential")
	}
	plain, err := c.aead.Open(nil, data[:c.aead.NonceSize()], data[c.aead.NonceSize():], nil)
	return string(plain), err
}
