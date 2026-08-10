package certmanager

import (
	"errors"
	"fmt"

	"goveto-edge/internal/node"
	"goveto-edge/internal/storage/gen/model"
)

const legacyPrivateKeyEnvelope = "enc:v1:"

func EncryptPrivateKey(cipher *node.CredentialCipher, clusterID, certificateID, privateKey string) (string, error) {
	value, err := cipher.EncryptScoped(privateKeyScope(clusterID, certificateID), privateKey)
	if err != nil {
		return "", err
	}
	return value, nil
}

func DecryptPrivateKey(cipher *node.CredentialCipher, certificate *model.Certificate) (string, error) {
	if certificate == nil {
		return "", errors.New("certificate is required")
	}
	if certificate.PrivateKeyEncrypted != "" {
		value := certificate.PrivateKeyEncrypted
		if len(value) >= len(legacyPrivateKeyEnvelope) && value[:len(legacyPrivateKeyEnvelope)] == legacyPrivateKeyEnvelope {
			value = value[len(legacyPrivateKeyEnvelope):]
		}
		plain, err := cipher.DecryptScoped(privateKeyScope(certificate.ClusterId, certificate.Id), value)
		if err != nil {
			return "", fmt.Errorf("decrypt certificate private key: %w", err)
		}
		return plain, nil
	}
	return "", errors.New("certificate private key is unavailable")
}

func privateKeyScope(clusterID, certificateID string) string {
	return "goveto-edge/certificate-private-key/v1\x00" + clusterID + "\x00" + certificateID
}

func RewrapPrivateKey(cipher *node.CredentialCipher, certificate *model.Certificate) (string, bool, error) {
	if certificate == nil || certificate.PrivateKeyEncrypted == "" {
		return "", false, errors.New("certificate private key is unavailable")
	}
	value := certificate.PrivateKeyEncrypted
	if len(value) >= len(legacyPrivateKeyEnvelope) && value[:len(legacyPrivateKeyEnvelope)] == legacyPrivateKeyEnvelope {
		value = value[len(legacyPrivateKeyEnvelope):]
	}
	return cipher.RewrapScoped(privateKeyScope(certificate.ClusterId, certificate.Id), value)
}
