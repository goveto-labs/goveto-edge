package logpush

import (
	"encoding/json"
	"fmt"

	"goveto-edge/internal/storage/gen/model"
)

// Credentials is the decrypted SASL credential payload stored as JSON in the
// credentials_encrypted column.
type Credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// MarshalCredentials serializes credentials for scoped encryption.
func MarshalCredentials(username, password string) (string, error) {
	encoded, err := json.Marshal(Credentials{Username: username, Password: password})
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// DestinationFromModel converts a stored destination into its runtime form.
// credentialsJSON is the decrypted credentials payload ("" when absent).
func DestinationFromModel(item *model.LogpushDestination, credentialsJSON string) (Destination, error) {
	brokers, err := ParseBrokers(item.Brokers)
	if err != nil {
		return Destination{}, fmt.Errorf("destination %s: %w", item.Id, err)
	}
	var logTypeList []string
	if len(item.LogTypes) > 0 {
		if err = json.Unmarshal(item.LogTypes, &logTypeList); err != nil {
			return Destination{}, fmt.Errorf("destination %s: log types: %w", item.Id, err)
		}
	}
	logTypes := make(map[string]bool, len(logTypeList))
	for _, logType := range logTypeList {
		logTypes[logType] = true
	}
	dest := Destination{
		ID:         item.Id,
		ClusterID:  item.ClusterId,
		Name:       item.Name,
		Brokers:    brokers,
		Topic:      item.Topic,
		LogTypes:   logTypes,
		TLSEnabled: item.TlsEnabled,
	}
	if item.SaslMechanism != nil {
		dest.SASLMechanism = *item.SaslMechanism
	}
	if credentialsJSON != "" {
		var credentials Credentials
		if err = json.Unmarshal([]byte(credentialsJSON), &credentials); err != nil {
			return Destination{}, fmt.Errorf("destination %s: credentials payload: %w", item.Id, err)
		}
		dest.Username = credentials.Username
		dest.Password = credentials.Password
	}
	return dest, nil
}
