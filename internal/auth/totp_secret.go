package auth

import (
	"context"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"

	"goveto-edge/internal/node"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/query"
)

const totpSecretScopeVersion = "auth/totp-secret/v1:user:"

var errTOTPSecretUnavailable = errors.New("TOTP secret is unavailable")

func totpSecretScope(userID string) string {
	return totpSecretScopeVersion + userID
}

func EncryptTOTPSecret(cipher *node.CredentialCipher, userID, secret string) (string, error) {
	if cipher == nil {
		return "", errTOTPSecretUnavailable
	}
	secret = strings.TrimSpace(secret)
	if userID == "" || secret == "" {
		return "", errors.New("TOTP user and secret are required")
	}
	return cipher.EncryptScoped(totpSecretScope(userID), secret)
}

func DecryptTOTPSecret(cipher *node.CredentialCipher, userID, stored string) (string, error) {
	if cipher == nil || userID == "" || !cipher.IsEnvelope(stored) {
		return "", errTOTPSecretUnavailable
	}
	secret, err := cipher.DecryptScoped(totpSecretScope(userID), stored)
	if err != nil {
		return "", fmt.Errorf("%w: %v", errTOTPSecretUnavailable, err)
	}
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return "", errTOTPSecretUnavailable
	}
	return secret, nil
}

func rewrapTOTPSecret(cipher *node.CredentialCipher, userID, stored string) (string, bool, error) {
	stored = strings.TrimSpace(stored)
	if stored == "" {
		return stored, false, nil
	}
	if cipher == nil || userID == "" {
		return "", false, errTOTPSecretUnavailable
	}
	if !cipher.IsEnvelope(stored) {
		decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(stored))
		if err != nil || len(decoded) == 0 {
			return "", false, fmt.Errorf("%w: unrecognized legacy TOTP secret", errTOTPSecretUnavailable)
		}
		wrapped, err := EncryptTOTPSecret(cipher, userID, stored)
		return wrapped, err == nil, err
	}
	if cipher.IsCurrent(stored) {
		if _, err := DecryptTOTPSecret(cipher, userID, stored); err != nil {
			return "", false, err
		}
		return stored, false, nil
	}
	wrapped, changed, err := cipher.RewrapScoped(totpSecretScope(userID), stored)
	if err != nil {
		return "", false, fmt.Errorf("%w: %v", errTOTPSecretUnavailable, err)
	}
	return wrapped, changed, nil
}

// RewrapTOTPSecrets migrates legacy plaintext seeds and ciphertexts written by
// previous keys before the old keys are removed from the configured keyring.
func RewrapTOTPSecrets(ctx context.Context, db *client.Client, cipher *node.CredentialCipher) error {
	type totpSecretRow struct {
		ID        string  `db:"id"`
		Encrypted *string `db:"totp_secret"`
	}
	users, err := client.Raw[totpSecretRow](ctx, db, "SELECT id, totp_secret FROM users WHERE totp_secret IS NOT NULL")
	if err != nil {
		return fmt.Errorf("read TOTP secrets for rewrap: %w", err)
	}
	for index := range users {
		user := &users[index]
		if user.Encrypted == nil {
			continue
		}
		wrapped, changed, rewrapErr := rewrapTOTPSecret(cipher, user.ID, *user.Encrypted)
		if rewrapErr != nil {
			return fmt.Errorf("rewrap TOTP secret for user %s: %w", user.ID, rewrapErr)
		}
		if changed {
			original := *user.Encrypted
			if _, err = db.User.Update().Where(
				query.User.Id.Equals(user.ID),
				query.User.TotpSecretEncrypted.Equals(&original),
			).Set(
				query.User.TotpSecretEncrypted.Set(wrapped),
			).DoMany(ctx); err != nil {
				return fmt.Errorf("persist rewrapped TOTP secret for user %s: %w", user.ID, err)
			}
		}
	}
	return nil
}
