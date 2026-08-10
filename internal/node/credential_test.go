package node

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestCredentialCipherRoundTrip(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	cipher, err := NewCredentialCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	one, err := cipher.Encrypt("communication-secret")
	if err != nil {
		t.Fatal(err)
	}
	two, err := cipher.Encrypt("communication-secret")
	if err != nil {
		t.Fatal(err)
	}
	if one == two {
		t.Fatal("encryption reused a nonce")
	}
	if !strings.HasPrefix(one, "enc:v2:"+cipher.KeyID()+":") {
		t.Fatalf("ciphertext does not identify its key: %q", one)
	}
	plain, err := cipher.Decrypt(one)
	if err != nil || plain != "communication-secret" {
		t.Fatalf("Decrypt() = %q, %v", plain, err)
	}
}

func TestCredentialCipherKeyringDecryptsPreviousAndLegacyValues(t *testing.T) {
	oldKey := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	newKey := base64.StdEncoding.EncodeToString([]byte("abcdef0123456789abcdef0123456789"))
	oldCipher, err := NewCredentialCipher(oldKey)
	if err != nil {
		t.Fatal(err)
	}
	oldEnvelope, err := oldCipher.EncryptScoped("scope", "old")
	if err != nil {
		t.Fatal(err)
	}
	legacy := strings.TrimPrefix(oldEnvelope, "enc:v2:"+oldCipher.KeyID()+":")

	rotated, err := NewCredentialCipherKeyring(newKey, oldKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{oldEnvelope, legacy} {
		plain, decryptErr := rotated.DecryptScoped("scope", value)
		if decryptErr != nil || plain != "old" {
			t.Fatalf("decrypt %q = %q, %v", value, plain, decryptErr)
		}
	}
	newEnvelope, err := rotated.Encrypt("new")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = oldCipher.Decrypt(newEnvelope); err == nil {
		t.Fatal("old keyring decrypted ciphertext written by the rotated primary")
	}
	rewrapped, changed, err := rotated.RewrapScoped("scope", oldEnvelope)
	if err != nil || !changed || !rotated.IsCurrent(rewrapped) {
		t.Fatalf("rewrap = %q, %v, %v", rewrapped, changed, err)
	}
	if again, changedAgain, err := rotated.RewrapScoped("scope", rewrapped); err != nil || changedAgain || again != rewrapped {
		t.Fatalf("current ciphertext was unnecessarily rewrapped: %q, %v, %v", again, changedAgain, err)
	}
}

func TestCredentialCipherDerivesStableDomainSeparatedSecrets(t *testing.T) {
	cipher, err := NewCredentialCipher("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	if err != nil {
		t.Fatal(err)
	}
	one := cipher.Derive("waf/site-1")
	two := cipher.Derive("waf/site-1")
	other := cipher.Derive("waf/site-2")
	if string(one) != string(two) || string(one) == string(other) || len(one) != 32 {
		t.Fatalf("unexpected derived secrets: one=%x two=%x other=%x", one, two, other)
	}
}

func TestCredentialCipherRejectsInvalidInput(t *testing.T) {
	if _, err := NewCredentialCipher(base64.StdEncoding.EncodeToString([]byte("short"))); err == nil {
		t.Fatal("expected invalid key length error")
	}
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	cipher, err := NewCredentialCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cipher.Decrypt("not-base64"); err == nil {
		t.Fatal("expected malformed ciphertext error")
	}
}

func TestRewrapSkipsEmptyValue(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	cipher, err := NewCredentialCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	wrapped, changed, err := cipher.Rewrap("")
	if err != nil {
		t.Fatalf("rewrap empty value: %v", err)
	}
	if changed {
		t.Fatal("empty value should not be reported as changed")
	}
	if wrapped != "" {
		t.Fatalf("empty value should be returned unchanged, got %q", wrapped)
	}
	// Scoped variant must behave the same way.
	scoped, scopedChanged, err := cipher.RewrapScoped("scope", "")
	if err != nil {
		t.Fatalf("rewrap scoped empty value: %v", err)
	}
	if scopedChanged || scoped != "" {
		t.Fatalf("scoped empty value should be a no-op, got %q changed=%v", scoped, scopedChanged)
	}
}
