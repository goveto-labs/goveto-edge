package dnssync

import (
	"testing"
	"time"

	"goveto-edge/internal/dnsprovider"
	"goveto-edge/internal/storage/gen/model"
)

func TestNodeDNSOfflineGracePeriod(t *testing.T) {
	now := time.Date(2026, 7, 28, 8, 0, 0, 0, time.UTC)
	heartbeat := now.Add(-time.Minute)
	recentlyOffline := now.Add(-NodeDNSOfflineGracePeriod + time.Second)
	graceBoundary := now.Add(-NodeDNSOfflineGracePeriod)
	expiredOffline := now.Add(-NodeDNSOfflineGracePeriod - time.Second)

	tests := []struct {
		name string
		node model.Node
		want bool
	}{
		{name: "online", node: model.Node{Status: model.NodeStatusONLINE}, want: true},
		{name: "offline within grace", node: model.Node{Status: model.NodeStatusOFFLINE, HeartbeatAt: &heartbeat, UpdatedAt: recentlyOffline}, want: true},
		{name: "offline at grace boundary", node: model.Node{Status: model.NodeStatusOFFLINE, HeartbeatAt: &heartbeat, UpdatedAt: graceBoundary}},
		{name: "offline after grace", node: model.Node{Status: model.NodeStatusOFFLINE, HeartbeatAt: &heartbeat, UpdatedAt: expiredOffline}},
		{name: "offline without heartbeat", node: model.Node{Status: model.NodeStatusOFFLINE}},
		{name: "disabled", node: model.Node{Status: model.NodeStatusDISABLED, HeartbeatAt: &heartbeat, UpdatedAt: recentlyOffline}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := nodeEligibleForDNS(test.node, now); got != test.want {
				t.Fatalf("nodeEligibleForDNS() = %v, want %v", got, test.want)
			}
		})
	}
}

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

func TestSameRecordSet(t *testing.T) {
	desired := []dnsprovider.Record{{Type: model.DNSRecordTypeAAAA, Value: "2001:0db8::1", Line: "DEFAULT"}}
	remote := []dnsprovider.Record{{Type: model.DNSRecordTypeAAAA, Value: "2001:db8::1", Line: "default"}}
	if !sameRecordSet(desired, remote) {
		t.Fatal("equivalent node record sets should not require synchronization")
	}
	if !sameRecordSet(nil, nil) {
		t.Fatal("two empty record sets should not require synchronization")
	}
	if sameRecordSet(nil, remote) {
		t.Fatal("a non-empty remote set must require synchronization")
	}
	if sameRecordSet(desired, append(remote, remote[0])) {
		t.Fatal("duplicate remote records must require synchronization")
	}
}

func TestRecordKeyNormalizesCase(t *testing.T) {
	left := key("EDGE.Example.com", model.DNSRecordTypeA, "192.0.2.1", "TELECOM")
	right := key("edge.example.com", model.DNSRecordTypeA, "192.0.2.1", "telecom")
	if left != right {
		t.Fatalf("equivalent record keys differ: %q != %q", left, right)
	}
}
