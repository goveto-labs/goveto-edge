package node

import (
	"context"
	"fmt"

	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/query"
)

func RewrapStoredSecrets(ctx context.Context, db *client.Client, cipher *CredentialCipher) error {
	credentials, err := db.NodeCredential.Query().Do(ctx)
	if err != nil {
		return err
	}
	for index := range credentials {
		credential := &credentials[index]
		if credential.BootstrapIdentityEncrypted == nil {
			continue
		}
		wrapped, changed, rewrapErr := cipher.Rewrap(*credential.BootstrapIdentityEncrypted)
		if rewrapErr != nil {
			return fmt.Errorf("rewrap node credential %s: %w", credential.NodeId, rewrapErr)
		}
		if changed {
			if _, err = db.NodeCredential.Update().Where(query.NodeCredential.NodeId.Equals(credential.NodeId)).Set(query.NodeCredential.BootstrapIdentityEncrypted.Set(wrapped)).Do(ctx); err != nil {
				return err
			}
		}
	}
	sshCredentials, err := db.SSHCredential.Query().Do(ctx)
	if err != nil {
		return err
	}
	for index := range sshCredentials {
		credential := &sshCredentials[index]
		wrapped, changed, rewrapErr := RewrapSSHCredentialSecret(cipher, credential)
		if rewrapErr != nil {
			return fmt.Errorf("rewrap SSH credential %s: %w", credential.Id, rewrapErr)
		}
		if changed {
			if _, err = db.SSHCredential.Update().Where(query.SSHCredential.Id.Equals(credential.Id)).Set(query.SSHCredential.SecretEncrypted.Set(wrapped)).Do(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}
