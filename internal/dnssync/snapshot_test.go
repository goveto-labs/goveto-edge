package dnssync

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestSameSnapshotRecordsIgnoresJSONBFormattingAndRecordOrder(t *testing.T) {
	current := []snapshotRecord{
		{Hostname: "b.example.com", Type: "AAAA", Value: "2001:db8::2", Line: "default", TTL: 300, NodeID: "node-2"},
		{Hostname: "a.example.com", Type: "A", Value: "192.0.2.1", Line: "telecom", TTL: 60, Proxied: true, ProviderRecordID: "record-1", DNSLineID: "line-1", NodeID: "node-1"},
	}

	// This mirrors JSONB output: object keys and whitespace differ from the
	// compact encoding produced by json.Marshal, and array order is reversed.
	persisted := json.RawMessage(`[
		{"ttl": 300, "line": "default", "type": "AAAA", "value": "2001:db8::2", "node_id": "node-2", "hostname": "b.example.com"},
		{"proxied": true, "dns_line_id": "line-1", "provider_record_id": "record-1", "node_id": "node-1", "ttl": 60, "value": "192.0.2.1", "type": "A", "line": "telecom", "hostname": "a.example.com"}
	]`)

	equal, err := sameSnapshotRecords(persisted, current)
	if err != nil {
		t.Fatalf("sameSnapshotRecords() error = %v", err)
	}
	if !equal {
		t.Fatal("semantically identical JSONB snapshot should be deduplicated")
	}
}

func TestSameSnapshotRecordsDetectsChangedRecord(t *testing.T) {
	current := []snapshotRecord{{Hostname: "a.example.com", Type: "A", Value: "192.0.2.1", Line: "default", TTL: 60}}
	persisted := json.RawMessage(`[{"hostname":"a.example.com","type":"A","value":"192.0.2.1","line":"default","ttl":300}]`)

	equal, err := sameSnapshotRecords(persisted, current)
	if err != nil {
		t.Fatalf("sameSnapshotRecords() error = %v", err)
	}
	if equal {
		t.Fatal("changed snapshot must not be deduplicated")
	}
}

func TestNormalizeSnapshotRecordsIsDeterministic(t *testing.T) {
	left := []snapshotRecord{
		{Hostname: "same.example.com", Type: "A", Value: "192.0.2.1", Line: "default", TTL: 300},
		{Hostname: "same.example.com", Type: "A", Value: "192.0.2.1", Line: "default", TTL: 60},
	}
	right := slices.Clone(left)
	slices.Reverse(right)

	normalizeSnapshotRecords(left)
	normalizeSnapshotRecords(right)
	if !slices.Equal(left, right) {
		t.Fatalf("normalized records differ: %#v != %#v", left, right)
	}
}

func TestSameSnapshotRecordsRejectsInvalidJSON(t *testing.T) {
	if equal, err := sameSnapshotRecords(json.RawMessage(`{"not":"an array"}`), nil); err == nil || equal {
		t.Fatalf("sameSnapshotRecords() = (%v, %v), want false and decode error", equal, err)
	}
}
