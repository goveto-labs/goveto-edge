package node

import (
	"encoding/base64"
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
	plain, err := cipher.Decrypt(one)
	if err != nil || plain != "communication-secret" {
		t.Fatalf("Decrypt() = %q, %v", plain, err)
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
