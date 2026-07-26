package node

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"

	"goveto-edge/internal/storage/gen/model"
)

type testInstallCause struct{ err error }

func (e *testInstallCause) Error() string { return "install cause" }
func (e *testInstallCause) Unwrap() error { return e.err }

func TestPermanentInstallErrorExposesClassificationAndCause(t *testing.T) {
	cause := &testInstallCause{err: context.Canceled}
	err := permanentInstallError(cause)
	if !errors.Is(err, errPermanentInstallConfiguration) {
		t.Fatalf("permanent classification is not visible: %v", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("underlying cancellation is not visible: %v", err)
	}
	var target *testInstallCause
	if !errors.As(err, &target) || target != cause {
		t.Fatalf("underlying typed cause is not visible: %v", err)
	}
	if permanentInstallError(nil) != nil {
		t.Fatal("nil cause should produce a nil error")
	}
}

func TestSSHCredentialSecretRoundTrip(t *testing.T) {
	cipher, err := NewCredentialCipher(
		base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")),
	)
	if err != nil {
		t.Fatal(err)
	}
	secret := SSHCredentialSecret{
		PrivateKeyPEM: "-----BEGIN PRIVATE KEY-----\ntest\n-----END PRIVATE KEY-----",
		Passphrase:    "passphrase",
	}
	encrypted, err := EncryptSSHCredentialSecret(cipher, "cluster-1", "credential-1", secret)
	if err != nil {
		t.Fatal(err)
	}
	credential := &model.SSHCredential{
		Id:              "credential-1",
		ClusterId:       "cluster-1",
		AuthType:        model.SSHAuthTypePRIVATE_KEY,
		SecretEncrypted: encrypted,
	}
	got, err := DecryptSSHCredentialSecret(cipher, credential)
	if err != nil {
		t.Fatal(err)
	}
	if got != secret {
		t.Fatalf("secret=%#v, want %#v", got, secret)
	}
	credential.ClusterId = "cluster-2"
	if _, err := DecryptSSHCredentialSecret(cipher, credential); err == nil {
		t.Fatal("expected cluster context mismatch")
	}
}

func TestSSHCredentialSecretValidation(t *testing.T) {
	if err := (SSHCredentialSecret{Password: "secret"}).Validate(model.SSHAuthTypePASSWORD); err != nil {
		t.Fatal(err)
	}
	if err := (SSHCredentialSecret{}).Validate(model.SSHAuthTypePASSWORD); err == nil {
		t.Fatal("expected missing password error")
	}
	if err := (SSHCredentialSecret{PrivateKeyPEM: "key"}).Validate(model.SSHAuthTypePRIVATE_KEY); err != nil {
		t.Fatal(err)
	}
	if err := (SSHCredentialSecret{Password: "secret", PrivateKeyPEM: "key"}).Validate(model.SSHAuthTypePRIVATE_KEY); err == nil {
		t.Fatal("expected mixed authentication fields error")
	}
}
