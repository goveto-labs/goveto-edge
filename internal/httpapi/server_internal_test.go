package httpapi

import (
	"context"
	"testing"

	"goveto-edge/internal/apikey"
	"goveto-edge/internal/audit"
)

type authFailureMemoryRecorder struct {
	entries []audit.Entry
}

func (recorder *authFailureMemoryRecorder) Record(_ context.Context, entry audit.Entry) error {
	recorder.entries = append(recorder.entries, entry)
	return nil
}

func TestAPIKeyAuthFailureRecorderBuildsSanitizedAuditEntry(t *testing.T) {
	recorder := &authFailureMemoryRecorder{}
	err := apiKeyAuthFailureRecorder(recorder)(context.Background(), apikey.AuthFailureEvent{
		Prefix: "gve1_Ab12cDe", SourceIP: "192.0.2.1", UserAgent: "client/1.0",
		RequestID: "request-1", FailureCode: "api_key_invalid",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(recorder.entries) != 1 {
		t.Fatalf("recorded %d entries, want 1", len(recorder.entries))
	}
	entry := recorder.entries[0]
	if entry.Actor != "api_key:gve1_Ab12cDe" || entry.ResourceID != "gve1_Ab12cDe" ||
		entry.Action != "auth.api_key" || entry.ResourceType != "api_key" ||
		entry.Result != audit.ResultFailure || entry.FailureReason != "api_key_invalid" ||
		entry.SourceIP != "192.0.2.1" || entry.UserAgent != "client/1.0" || entry.RequestID != "request-1" {
		t.Fatalf("unexpected entry: %#v", entry)
	}
}
