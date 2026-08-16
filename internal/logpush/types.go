// Package logpush delivers agent log records to external destinations
// (Kafka) with best-effort, non-blocking semantics.
package logpush

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
)

// Log record types a destination may subscribe to. These mirror the record
// types produced by edge agents (see internal/edgeprotocol).
const (
	LogTypeAccess       = "access"
	LogTypeCaddy        = "caddy"
	LogTypeNodeRuntime  = "node_runtime"
	LogTypeOriginHealth = "origin_health"
)

// SASL mechanisms supported for Kafka destinations.
const (
	SASLNone        = ""
	SASLPlain       = "plain"
	SASLSCRAMSHA256 = "scram-sha-256"
	SASLSCRAMSHA512 = "scram-sha-512"
)

const (
	brokersInputLimit = 2048
	topicPattern      = `^[a-zA-Z0-9._-]{1,249}$`

	// MaxDestinationsPerCluster bounds Kafka clients, goroutines, and buffered
	// log data created by one cluster.
	MaxDestinationsPerCluster = 8
)

var topicRegex = regexp.MustCompile(topicPattern)

// Destination is the runtime, decrypted form of a logpush destination.
type Destination struct {
	ID            string
	ClusterID     string
	Name          string
	Brokers       []string
	Topic         string
	LogTypes      map[string]bool
	TLSEnabled    bool
	SASLMechanism string
	Username      string
	Password      string
}

// Wants reports whether the destination subscribes to the given record type.
func (d Destination) Wants(recordType string) bool {
	return d.LogTypes[recordType]
}

// ValidLogType reports whether recordType is a known log type.
func ValidLogType(recordType string) bool {
	switch recordType {
	case LogTypeAccess, LogTypeCaddy, LogTypeNodeRuntime, LogTypeOriginHealth:
		return true
	default:
		return false
	}
}

// ValidSASLMechanism reports whether mechanism is a supported SASL mechanism
// (empty means no SASL).
func ValidSASLMechanism(mechanism string) bool {
	switch mechanism {
	case SASLNone, SASLPlain, SASLSCRAMSHA256, SASLSCRAMSHA512:
		return true
	default:
		return false
	}
}

// ParseBrokers validates a comma-separated host:port list and returns the
// individual broker addresses.
func ParseBrokers(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("brokers are required")
	}
	if len(raw) > brokersInputLimit {
		return nil, errors.New("brokers list is too long")
	}
	parts := strings.Split(raw, ",")
	brokers := make([]string, 0, len(parts))
	for _, part := range parts {
		broker := strings.TrimSpace(part)
		if broker == "" {
			return nil, errors.New("brokers must not contain empty entries")
		}
		host, port, err := net.SplitHostPort(broker)
		if err != nil {
			return nil, fmt.Errorf("broker %q must be in host:port form", broker)
		}
		if strings.TrimSpace(host) == "" {
			return nil, fmt.Errorf("broker %q has an empty host", broker)
		}
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			return nil, fmt.Errorf("broker %q has an invalid port", broker)
		}
		brokers = append(brokers, broker)
	}
	return brokers, nil
}

// ValidateTopic checks the value is a legal Kafka topic name.
func ValidateTopic(topic string) error {
	if topic == "." || topic == ".." || !topicRegex.MatchString(topic) {
		return errors.New(
			"topic must be 1-249 characters of letters, digits, dots, underscores or dashes, and cannot be . or ..",
		)
	}
	return nil
}
