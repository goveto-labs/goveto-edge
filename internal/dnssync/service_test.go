package dnssync

import (
	"testing"

	"goveto-edge/internal/storage/gen/model"
)

func TestNormalizeLineKey(t *testing.T) {
	tests := map[string]string{
		"":          "default",
		" DEFAULT ": "default",
		" Telecom ": "telecom",
	}
	for input, want := range tests {
		if got := normalizeLineKey(input); got != want {
			t.Fatalf("normalizeLineKey(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestRecordKeyNormalizesCase(t *testing.T) {
	left := key("EDGE.Example.com", model.DNSRecordTypeA, "192.0.2.1", "TELECOM")
	right := key("edge.example.com", model.DNSRecordTypeA, "192.0.2.1", "telecom")
	if left != right {
		t.Fatalf("equivalent record keys differ: %q != %q", left, right)
	}
}
