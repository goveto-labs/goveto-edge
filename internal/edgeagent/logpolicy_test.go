package edgeagent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLogPolicyRedactsBeforeQueueing(t *testing.T) {
	policy := LogPolicy{
		SampleRate: 1, RedactQuery: true, AnonymizeIP: true,
		RedactedHeaders: map[string]struct{}{"authorization": {}},
	}
	payload := []byte(`{"request":{"uri":"/account?token=secret","client_ip":"192.0.2.129","remote_ip":"2001:db8:1:2::9","headers":{"Authorization":["Bearer secret"],"Accept":["text/html"]}},"status":200}`)
	redacted, keep := policy.Apply(payload)
	if !keep {
		t.Fatal("full sampling policy dropped record")
	}
	text := string(redacted)
	for _, secret := range []string{"token=secret", "Bearer secret", "192.0.2.129", "2001:db8:1:2::9"} {
		if strings.Contains(text, secret) {
			t.Fatalf("redacted payload contains %q: %s", secret, text)
		}
	}
	var event map[string]any
	if err := json.Unmarshal(redacted, &event); err != nil {
		t.Fatal(err)
	}
}

func TestLogPolicySamplingIsStable(t *testing.T) {
	payload := []byte(`{"request":{"uri":"/"},"status":200}`)
	policy := LogPolicy{SampleRate: 0.5}
	_, first := policy.Apply(payload)
	for range 20 {
		_, current := policy.Apply(payload)
		if current != first {
			t.Fatal("same event had an unstable sampling decision")
		}
	}
	if _, keep := (LogPolicy{SampleRate: 0}).Apply(payload); keep {
		t.Fatal("zero sampling rate retained an event")
	}
}

func TestDefaultLogPolicyRedactsForwardedClientAddresses(t *testing.T) {
	t.Setenv("EDGE_AGENT_LOG_REDACT_HEADERS", "")
	policy := logPolicyFromEnv()
	payload := []byte(`{"request":{"uri":"/","client_ip":"192.0.2.10","headers":{"X-Forwarded-For":["198.51.100.7"],"CF-Connecting-IP":["203.0.113.9"]}},"status":200}`)
	redacted, keep := policy.Apply(payload)
	if !keep {
		t.Fatal("default sampling dropped an event")
	}
	for _, address := range []string{"198.51.100.7", "203.0.113.9"} {
		if strings.Contains(string(redacted), address) {
			t.Fatalf("default policy leaked forwarded client address %s: %s", address, redacted)
		}
	}
}
