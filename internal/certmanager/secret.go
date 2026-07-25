package certmanager

import (
	"errors"
	"fmt"

	"goveto-edge/internal/node"
	"goveto-edge/internal/storage/gen/model"
)

const privateKeyEnvelope = "enc:v1:"

func EncryptPrivateKey(cipher *node.CredentialCipher, clusterID, certificateID, privateKey string) (string, error) {
	value, err := cipher.EncryptScoped(privateKeyScope(clusterID, certificateID), privateKey)
	if err != nil {
		return "", err
	}
	return privateKeyEnvelope + value, nil
}

func DecryptPrivateKey(cipher *node.CredentialCipher, certificate *model.Certificate) (string, error) {
	if certificate == nil {
		return "", errors.New("certificate is required")
	}
	if certificate.PrivateKeyEncrypted != nil && *certificate.PrivateKeyEncrypted != "" {
		value := *certificate.PrivateKeyEncrypted
		if len(value) < len(privateKeyEnvelope) || value[:len(privateKeyEnvelope)] != privateKeyEnvelope {
			return "", errors.New("unsupported certificate private key envelope")
		}
		plain, err := cipher.DecryptScoped(privateKeyScope(certificate.ClusterId, certificate.Id), value[len(privateKeyEnvelope):])
		if err != nil {
			return "", fmt.Errorf("decrypt certificate private key: %w", err)
		}
		return plain, nil
	}
	if certificate.PrivateKeyPem != nil && *certificate.PrivateKeyPem != "" {
		return *certificate.PrivateKeyPem, nil
	}
	return "", errors.New("certificate private key is unavailable")
}

func privateKeyScope(clusterID, certificateID string) string {
	return "goveto-edge/certificate-private-key/v1\x00" + clusterID + "\x00" + certificateID
}
