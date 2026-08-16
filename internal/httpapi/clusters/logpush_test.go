package clusters

import (
	"encoding/json"
	"testing"
	"time"

	"goveto-edge/internal/logpush"
	"goveto-edge/internal/storage/gen/model"
)

func stringPtr(value string) *string { return &value }

func TestValidateLogpushDestinationInput(t *testing.T) {
	tests := []struct {
		name          string
		input         logpushDestinationRequest
		requireTarget bool
		wantError     bool
	}{
		{
			name: "minimal create",
			input: logpushDestinationRequest{
				Name: "warehouse", Brokers: "kafka-1:9092, kafka-2:9092", Topic: "edge-logs",
			},
			requireTarget: true,
		},
		{
			name: "create with SASL",
			input: logpushDestinationRequest{
				Name: "secure", Brokers: "kafka:9093", Topic: "logs",
				SASLMechanism: stringPtr("scram-sha-512"), Username: "edge", Password: "secret",
			},
			requireTarget: true,
		},
		{
			name:          "update keeps target",
			input:         logpushDestinationRequest{Name: "renamed"},
			requireTarget: false,
		},
		{
			name:          "missing name",
			input:         logpushDestinationRequest{Brokers: "kafka:9092", Topic: "logs"},
			requireTarget: true,
			wantError:     true,
		},
		{
			name:          "missing brokers on create",
			input:         logpushDestinationRequest{Name: "x", Topic: "logs"},
			requireTarget: true,
			wantError:     true,
		},
		{
			name:          "broker without port",
			input:         logpushDestinationRequest{Name: "x", Brokers: "kafka", Topic: "logs"},
			requireTarget: true,
			wantError:     true,
		},
		{
			name:          "broker with bad port",
			input:         logpushDestinationRequest{Name: "x", Brokers: "kafka:99999", Topic: "logs"},
			requireTarget: true,
			wantError:     true,
		},
		{
			name:          "invalid topic",
			input:         logpushDestinationRequest{Name: "x", Brokers: "kafka:9092", Topic: "bad topic!"},
			requireTarget: true,
			wantError:     true,
		},
		{
			name:          "dot topic",
			input:         logpushDestinationRequest{Name: "x", Brokers: "kafka:9092", Topic: "."},
			requireTarget: true,
			wantError:     true,
		},
		{
			name:          "dot dot topic",
			input:         logpushDestinationRequest{Name: "x", Brokers: "kafka:9092", Topic: ".."},
			requireTarget: true,
			wantError:     true,
		},
		{
			name: "unknown log type",
			input: logpushDestinationRequest{
				Name: "x", Brokers: "kafka:9092", Topic: "logs", LogTypes: []string{"access", "bogus"},
			},
			requireTarget: true,
			wantError:     true,
		},
		{
			name: "empty log types",
			input: logpushDestinationRequest{
				Name: "x", Brokers: "kafka:9092", Topic: "logs", LogTypes: []string{},
			},
			requireTarget: true,
			wantError:     true,
		},
		{
			name: "sasl without credentials on create",
			input: logpushDestinationRequest{
				Name: "x", Brokers: "kafka:9092", Topic: "logs", SASLMechanism: stringPtr("plain"),
			},
			requireTarget: true,
			wantError:     true,
		},
		{
			name: "password without username",
			input: logpushDestinationRequest{
				Name: "x", Brokers: "kafka:9092", Topic: "logs", Password: "secret",
			},
			requireTarget: true,
			wantError:     true,
		},
		{
			name: "credentials without mechanism",
			input: logpushDestinationRequest{
				Name: "x", Brokers: "kafka:9092", Topic: "logs", Username: "edge", Password: "secret",
			},
			requireTarget: true,
			wantError:     true,
		},
		{
			name: "credentials with none mechanism",
			input: logpushDestinationRequest{
				Name: "x", Brokers: "kafka:9092", Topic: "logs", SASLMechanism: stringPtr("none"),
				Username: "edge", Password: "secret",
			},
			requireTarget: true,
			wantError:     true,
		},
		{
			name: "unknown mechanism",
			input: logpushDestinationRequest{
				Name: "x", Brokers: "kafka:9092", Topic: "logs", SASLMechanism: stringPtr("oauthbearer"),
			},
			requireTarget: true,
			wantError:     true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalized, err := validateLogpushDestinationInput(test.input, test.requireTarget)
			if (err != nil) != test.wantError {
				t.Fatalf("validate error = %v, wantError %t", err, test.wantError)
			}
			if test.wantError {
				return
			}
			if normalized.name == "" {
				t.Fatal("normalized name must not be empty")
			}
			if test.requireTarget && len(normalized.brokers) == 0 {
				t.Fatal("create validation must require brokers")
			}
		})
	}
}

func TestValidateLogpushDestinationInputDefaults(t *testing.T) {
	normalized, err := validateLogpushDestinationInput(logpushDestinationRequest{
		Name: "warehouse", Brokers: "kafka:9092", Topic: "logs",
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	var logTypes []string
	if err = json.Unmarshal(normalized.logTypesJSON, &logTypes); err != nil {
		t.Fatal(err)
	}
	if len(logTypes) != 1 || logTypes[0] != logpush.LogTypeAccess {
		t.Fatalf("default log types = %v, want [access]", logTypes)
	}
	if normalized.mechanism != logpush.SASLNone {
		t.Fatalf("default mechanism = %q, want none", normalized.mechanism)
	}
}

func TestValidateLogpushDestinationInputNoneClearsMechanism(t *testing.T) {
	normalized, err := validateLogpushDestinationInput(logpushDestinationRequest{
		Name: "x", SASLMechanism: stringPtr("none"),
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.mechanism != logpush.SASLNone {
		t.Fatalf("mechanism = %q, want cleared", normalized.mechanism)
	}
}

func TestLogpushDestinationResponseDoesNotExposeCredentials(t *testing.T) {
	now := time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC)
	ciphertext := "secret-ciphertext"
	mechanism := "plain"
	response := newLogpushDestinationResponse(&model.LogpushDestination{
		Id: "dest", ClusterId: "cluster", Name: "Warehouse", Type: model.LogpushTypeKAFKA,
		Brokers: "kafka-1:9092,kafka-2:9092", Topic: "edge-logs",
		LogTypes:             []byte(`["access","node_runtime"]`),
		TlsEnabled:           true,
		SaslMechanism:        &mechanism,
		CredentialsEncrypted: &ciphertext,
		Enabled:              true,
		CreatedAt:            now,
		UpdatedAt:            now,
	})
	if !response.CredentialsConfigured {
		t.Fatal("credentials_configured must be true when ciphertext is stored")
	}
	if len(response.Brokers) != 2 {
		t.Fatalf("brokers = %v", response.Brokers)
	}
	if len(response.LogTypes) != 2 {
		t.Fatalf("log types = %v", response.LogTypes)
	}
	if response.SASLMechanism == ciphertext || response.Topic == ciphertext {
		t.Fatal("response must not contain ciphertext")
	}
}

func TestLogpushDestinationScopeSeparatesClustersAndDestinations(t *testing.T) {
	first := logpush.CredentialScope("cluster-a", "dest-a")
	for _, other := range []string{
		logpush.CredentialScope("cluster-b", "dest-a"),
		logpush.CredentialScope("cluster-a", "dest-b"),
	} {
		if first == other {
			t.Fatalf("scope %q must differ from %q", first, other)
		}
	}
}
