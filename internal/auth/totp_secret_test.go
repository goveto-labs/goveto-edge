package auth

import (
	"encoding/base32"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"goveto-edge/internal/node"
)

const testTOTPSeed = "JBSWY3DPEHPK3PXP"

func testTOTPCipher(t *testing.T, key string, previous ...string) *node.CredentialCipher {
	t.Helper()
	cipher, err := node.NewCredentialCipherKeyring(key, previous...)
	if err != nil {
		t.Fatal(err)
	}
	return cipher
}

func TestRewrapTOTPSecretRowsSkipsCorruptUsersAndContinues(t *testing.T) {
	corrupt := "corrupt"
	legacy := testTOTPSeed
	persisted := map[string]string{}
	result, err := rewrapTOTPSecretRows([]totpSecretRow{
		{ID: "bad-user", Encrypted: &corrupt},
		{ID: "good-user", Encrypted: &legacy},
	}, func(userID, stored string) (string, bool, error) {
		if userID == "bad-user" {
			return "", false, errTOTPSecretUnavailable
		}
		return "wrapped:" + stored, true, nil
	}, func(userID, _, wrapped string) error {
		persisted[userID] = wrapped
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Skipped) != 1 || result.Skipped[0].UserID != "bad-user" ||
		!errors.Is(result.Skipped[0].Err, errTOTPSecretUnavailable) {
		t.Fatalf("unexpected skipped users: %#v", result.Skipped)
	}
	if persisted["good-user"] != "wrapped:"+testTOTPSeed {
		t.Fatalf("valid user was not rewrapped after corrupt user: %#v", persisted)
	}
}

func TestRewrapTOTPSecretRowsReturnsPersistenceErrors(t *testing.T) {
	legacy := testTOTPSeed
	persistErr := errors.New("database unavailable")
	_, err := rewrapTOTPSecretRows([]totpSecretRow{{ID: "user-1", Encrypted: &legacy}},
		func(_, stored string) (string, bool, error) { return "wrapped:" + stored, true, nil },
		func(_, _, _ string) error { return persistErr },
	)
	if !errors.Is(err, persistErr) {
		t.Fatalf("rewrap error = %v, want %v", err, persistErr)
	}
}

func TestRewrapTOTPSecretsRejectsMissingCipher(t *testing.T) {
	if _, err := RewrapTOTPSecrets(t.Context(), nil, nil); !errors.Is(err, errTOTPSecretUnavailable) {
		t.Fatalf("RewrapTOTPSecrets() error = %v, want %v", err, errTOTPSecretUnavailable)
	}
}

func TestTOTPSecretCiphertextDoesNotExposeSeed(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	cipher := testTOTPCipher(t, key)
	encrypted, err := EncryptTOTPSecret(cipher, "user-1", testTOTPSeed)
	if err != nil {
		t.Fatal(err)
	}
	if encrypted == testTOTPSeed || strings.Contains(encrypted, testTOTPSeed) || !cipher.IsEnvelope(encrypted) {
		t.Fatalf("TOTP secret was not stored in an encrypted envelope: %q", encrypted)
	}
	plain, err := DecryptTOTPSecret(cipher, "user-1", encrypted)
	if err != nil || plain != testTOTPSeed {
		t.Fatalf("DecryptTOTPSecret() = %q, %v", plain, err)
	}
}

func TestTOTPSecretCiphertextIsBoundToUser(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	cipher := testTOTPCipher(t, key)
	encrypted, err := EncryptTOTPSecret(cipher, "user-1", testTOTPSeed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecryptTOTPSecret(cipher, "user-2", encrypted); err == nil {
		t.Fatal("TOTP ciphertext decrypted for a different user")
	}
}

func TestTOTPSecretTamperingFailsClosed(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	cipher := testTOTPCipher(t, key)
	encrypted, err := EncryptTOTPSecret(cipher, "user-1", testTOTPSeed)
	if err != nil {
		t.Fatal(err)
	}
	payloadStart := strings.LastIndexByte(encrypted, ':') + 1
	payload, err := base64.RawStdEncoding.DecodeString(encrypted[payloadStart:])
	if err != nil {
		t.Fatal(err)
	}
	payload[len(payload)-1] ^= 1
	tampered := encrypted[:payloadStart] + base64.RawStdEncoding.EncodeToString(payload)
	if _, err = DecryptTOTPSecret(cipher, "user-1", tampered); err == nil {
		t.Fatal("tampered TOTP ciphertext decrypted successfully")
	}
	if _, err = DecryptTOTPSecret(cipher, "user-1", testTOTPSeed); err == nil {
		t.Fatal("normal TOTP decryption accepted a legacy plaintext seed")
	}
}

func TestTOTPSecretRewrapMigratesPlaintextAndPreviousKey(t *testing.T) {
	oldKey := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	newKey := base64.StdEncoding.EncodeToString([]byte("abcdef0123456789abcdef0123456789"))
	oldCipher := testTOTPCipher(t, oldKey)
	rotated := testTOTPCipher(t, newKey, oldKey)

	oldEncrypted, err := EncryptTOTPSecret(oldCipher, "user-1", testTOTPSeed)
	if err != nil {
		t.Fatal(err)
	}
	for _, stored := range []string{testTOTPSeed, oldEncrypted} {
		rewrapped, changed, rewrapErr := rewrapTOTPSecret(rotated, "user-1", stored)
		if rewrapErr != nil || !changed || !rotated.IsCurrent(rewrapped) {
			t.Fatalf("rewrap %q = %q, %v, %v", stored, rewrapped, changed, rewrapErr)
		}
		plain, decryptErr := DecryptTOTPSecret(rotated, "user-1", rewrapped)
		if decryptErr != nil || plain != testTOTPSeed {
			t.Fatalf("decrypt rewrapped value = %q, %v", plain, decryptErr)
		}
	}

	current, err := EncryptTOTPSecret(rotated, "user-1", testTOTPSeed)
	if err != nil {
		t.Fatal(err)
	}
	if rewrapped, changed, rewrapErr := rewrapTOTPSecret(rotated, "user-1", current); rewrapErr != nil || changed || rewrapped != current {
		t.Fatalf("current-key rewrap = %q, %v, %v", rewrapped, changed, rewrapErr)
	}
	if _, _, rewrapErr := rewrapTOTPSecret(rotated, "user-2", current); rewrapErr == nil {
		t.Fatal("current-key rewrap accepted ciphertext bound to a different user")
	}

	unpaddedSeed := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("sixteen-byte-key"))
	if len(unpaddedSeed)%8 == 0 {
		t.Fatalf("test seed unexpectedly has padded base32 length: %q", unpaddedSeed)
	}
	if rewrapped, changed, rewrapErr := rewrapTOTPSecret(rotated, "user-1", unpaddedSeed); rewrapErr != nil || !changed {
		t.Fatalf("rewrap unpadded seed = %q, %v, %v", rewrapped, changed, rewrapErr)
	}
}

func TestTOTPSecretRewrapRejectsUnrecognizedValues(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	cipher := testTOTPCipher(t, key)
	for _, stored := range []string{
		"not-a-base32-seed",
		"enc:v3:unknown:payload",
		"enc:v2:malformed",
	} {
		if _, _, err := rewrapTOTPSecret(cipher, "user-1", stored); err == nil {
			t.Fatalf("rewrap accepted unrecognized value %q", stored)
		}
	}
}
