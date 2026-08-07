package node

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"testing"

	"golang.org/x/crypto/ssh"

	"goveto-edge/internal/storage/gen/model"
)

func testHostKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestCaptureHostKeyRecordsPresentedKey(t *testing.T) {
	key := testHostKey(t)
	settings, captured := CaptureHostKey()
	if captured() != nil {
		t.Fatal("captured key before callback invocation should be nil")
	}
	if err := settings.Callback("example:22", nil, key); err != nil {
		t.Fatalf("capture callback error = %v", err)
	}
	got := captured()
	if got == nil {
		t.Fatal("captured key after callback invocation is nil")
	}
	_, wantPublic, wantFingerprint := hostKeyParts(key)
	_, gotPublic, gotFingerprint := hostKeyParts(got)
	if gotPublic != wantPublic || gotFingerprint != wantFingerprint {
		t.Fatalf("captured key = %s %s, want %s %s", gotPublic, gotFingerprint, wantPublic, wantFingerprint)
	}
}

func TestHostKeyPartsUsesAuthorizedKeyFormatAndSHA256Fingerprint(t *testing.T) {
	key := testHostKey(t)
	keyType, publicKey, fingerprint := hostKeyParts(key)
	if keyType != "ssh-ed25519" {
		t.Fatalf("key type = %q", keyType)
	}
	parsed, _, _, _, err := ssh.ParseAuthorizedKey([]byte(publicKey))
	if err != nil {
		t.Fatalf("stored public key does not round-trip through authorized_keys format: %v", err)
	}
	if ssh.FingerprintSHA256(parsed) != fingerprint {
		t.Fatalf("fingerprint = %q, want %q", fingerprint, ssh.FingerprintSHA256(parsed))
	}
}

func TestSSHHostKeySummaryMatchesParts(t *testing.T) {
	key := testHostKey(t)
	keyType, fingerprint := SSHHostKeySummary(key)
	wantType, _, wantFingerprint := hostKeyParts(key)
	if keyType != wantType || fingerprint != wantFingerprint {
		t.Fatalf("summary = %q %q, want %q %q", keyType, fingerprint, wantType, wantFingerprint)
	}
	descType, descPublic, descFingerprint := DescribeHostKey(key)
	_, wantPublic, _ := hostKeyParts(key)
	if descType != wantType || descPublic != wantPublic || descFingerprint != wantFingerprint {
		t.Fatalf("describe = %q %q %q", descType, descPublic, descFingerprint)
	}
}

func TestHostKeyMismatchErrorIsPermanentAndDetectable(t *testing.T) {
	err := fmt.Errorf("wrap: %w", HostKeyMismatchError{Expected: "SHA256:a", Actual: "SHA256:b"})
	if !IsHostKeyMismatch(err) {
		t.Fatal("IsHostKeyMismatch did not detect wrapped mismatch")
	}
	if !IsHostKeyIssue(err) {
		t.Fatal("IsHostKeyIssue did not detect mismatch")
	}
	if !errors.Is(err, errPermanentInstallConfiguration) {
		t.Fatal("host key mismatch must be a permanent install error to stop retries")
	}
}

func TestHostKeyNotPinnedErrorIsPermanentAndDetectable(t *testing.T) {
	err := fmt.Errorf("wrap: %w", HostKeyNotPinnedError{})
	if !IsHostKeyNotPinned(err) {
		t.Fatal("IsHostKeyNotPinned did not detect wrapped error")
	}
	if !IsHostKeyIssue(err) {
		t.Fatal("IsHostKeyIssue did not detect missing pin")
	}
	if !errors.Is(err, errPermanentInstallConfiguration) {
		t.Fatal("missing pin must be a permanent install error to stop retries")
	}
}

func TestCheckPresentedHostKeyMatchMismatchAndMissingPin(t *testing.T) {
	key := testHostKey(t)
	other := testHostKey(t)
	_, publicKey, fingerprint := hostKeyParts(key)
	pinned := &model.NodeSSHHostKey{
		PublicKey:         publicKey,
		FingerprintSha256: fingerprint,
		KeyType:           key.Type(),
	}
	if err := checkPresentedHostKey(nil, key); !IsHostKeyNotPinned(err) {
		t.Fatalf("nil pin error = %v", err)
	}
	if err := checkPresentedHostKey(pinned, key); err != nil {
		t.Fatalf("matching key error = %v", err)
	}
	err := checkPresentedHostKey(pinned, other)
	if !IsHostKeyMismatch(err) {
		t.Fatalf("mismatch error = %v", err)
	}
	var mismatch HostKeyMismatchError
	if !errors.As(err, &mismatch) || mismatch.Expected != fingerprint {
		t.Fatalf("mismatch details = %+v", mismatch)
	}
}
