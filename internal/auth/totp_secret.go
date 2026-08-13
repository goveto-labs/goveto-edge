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

// TOTPRewrapFailure identifies a user whose stored TOTP secret could not be migrated.
type TOTPRewrapFailure struct {
	UserID string
	Err    error
}

// TOTPRewrapResult reports corrupt per-user secrets skipped during migration.
type TOTPRewrapResult struct {
	Skipped []TOTPRewrapFailure
}

type totpSecretRow struct {
	ID        string  `db:"id"`
	Encrypted *string `db:"totp_secret"`
}

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
func RewrapTOTPSecrets(ctx context.Context, db *client.Client, cipher *node.CredentialCipher) (TOTPRewrapResult, error) {
	if cipher == nil {
		return TOTPRewrapResult{}, errTOTPSecretUnavailable
	}
	users, err := client.Raw[totpSecretRow](ctx, db, "SELECT id, totp_secret FROM users WHERE totp_secret IS NOT NULL")
	if err != nil {
		return TOTPRewrapResult{}, fmt.Errorf("read TOTP secrets for rewrap: %w", err)
	}
	return rewrapTOTPSecretRows(users, func(userID, stored string) (string, bool, error) {
		return rewrapTOTPSecret(cipher, userID, stored)
	}, func(userID, original, wrapped string) error {
		_, updateErr := db.User.Update().Where(
			query.User.Id.Equals(userID),
			query.User.TotpSecretEncrypted.Equals(&original),
		).Set(
			query.User.TotpSecretEncrypted.Set(wrapped),
		).DoMany(ctx)
		return updateErr
	})
}

func rewrapTOTPSecretRows(
	users []totpSecretRow,
	rewrap func(userID, stored string) (string, bool, error),
	persist func(userID, original, wrapped string) error,
) (TOTPRewrapResult, error) {
	result := TOTPRewrapResult{}
	for index := range users {
		user := &users[index]
		if user.Encrypted == nil {
			continue
		}
		wrapped, changed, rewrapErr := rewrap(user.ID, *user.Encrypted)
		if rewrapErr != nil {
			result.Skipped = append(result.Skipped, TOTPRewrapFailure{UserID: user.ID, Err: rewrapErr})
			continue
		}
		if changed {
			original := *user.Encrypted
			if err := persist(user.ID, original, wrapped); err != nil {
				return result, fmt.Errorf("persist rewrapped TOTP secret for user %s: %w", user.ID, err)
			}
		}
	}
	return result, nil
}
