package node

import (
	"encoding/base64"
	"testing"

	"goveto-edge/internal/storage/gen/model"
)

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
