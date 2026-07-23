package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

type SSHCredentialSecret struct {
	Password      string `json:"password,omitempty"`
	PrivateKeyPEM string `json:"private_key,omitempty"`
	Passphrase    string `json:"passphrase,omitempty"`
}

func (s SSHCredentialSecret) Validate(authType model.SSHAuthType) error {
	switch authType {
	case model.SSHAuthTypePASSWORD:
		if s.Password == "" {
			return errors.New("password is required for password authentication")
		}
		if s.PrivateKeyPEM != "" || s.Passphrase != "" {
			return errors.New("private key fields are not allowed for password authentication")
		}
	case model.SSHAuthTypePRIVATE_KEY:
		if strings.TrimSpace(s.PrivateKeyPEM) == "" {
			return errors.New("private_key is required for private key authentication")
		}
		if s.Password != "" {
			return errors.New("password is not allowed for private key authentication")
		}
	default:
		return errors.New("auth_type must be PASSWORD or PRIVATE_KEY")
	}
	return nil
}

func SSHCredentialEncryptionContext(clusterID, credentialID string) string {
	return "ssh-credential:v1:" + clusterID + ":" + credentialID
}

func EncryptSSHCredentialSecret(
	cipher *CredentialCipher,
	clusterID, credentialID string,
	secret SSHCredentialSecret,
) (string, error) {
	plain, err := json.Marshal(secret)
	if err != nil {
		return "", err
	}
	return cipher.EncryptScoped(
		SSHCredentialEncryptionContext(clusterID, credentialID),
		string(plain),
	)
}

func DecryptSSHCredentialSecret(
	cipher *CredentialCipher,
	credential *model.SSHCredential,
) (SSHCredentialSecret, error) {
	plain, err := cipher.DecryptScoped(
		SSHCredentialEncryptionContext(credential.ClusterId, credential.Id),
		credential.SecretEncrypted,
	)
	if err != nil {
		return SSHCredentialSecret{}, err
	}
	var secret SSHCredentialSecret
	if err := json.Unmarshal([]byte(plain), &secret); err != nil {
		return SSHCredentialSecret{}, fmt.Errorf("decode SSH credential secret: %w", err)
	}
	if err := secret.Validate(credential.AuthType); err != nil {
		return SSHCredentialSecret{}, fmt.Errorf("validate SSH credential secret: %w", err)
	}
	return secret, nil
}

func ResolveSSHInstallInput(
	ctx context.Context,
	db *client.Client,
	cipher *CredentialCipher,
	clusterID string,
	reference SSHInstallReference,
) (*model.SSHCredential, SSHInstallInput, error) {
	if err := reference.Validate(); err != nil {
		return nil, SSHInstallInput{}, err
	}
	credential, err := db.SSHCredential.FindUnique(
		ctx,
		query.SSHCredential.Id.Equals(reference.CredentialID),
	)
	if err != nil {
		return nil, SSHInstallInput{}, err
	}
	if credential == nil || credential.ClusterId != clusterID {
		return nil, SSHInstallInput{}, errors.New("SSH credential not found")
	}
	secret, err := DecryptSSHCredentialSecret(cipher, credential)
	if err != nil {
		return nil, SSHInstallInput{}, fmt.Errorf("decrypt SSH credential: %w", err)
	}
	input := SSHInstallInput{
		EntryIP:       reference.EntryIP,
		Port:          reference.Port,
		User:          credential.Username,
		Password:      secret.Password,
		PrivateKeyPEM: secret.PrivateKeyPEM,
		Passphrase:    secret.Passphrase,
	}
	if err := input.Validate(); err != nil {
		return nil, SSHInstallInput{}, err
	}
	return credential, input, nil
}
