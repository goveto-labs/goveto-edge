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
