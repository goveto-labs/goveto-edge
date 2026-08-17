package dnssync

import (
	"context"
	"fmt"
	"reflect"
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

func TestRecordKeyNormalizesEquivalentIPv6(t *testing.T) {
	left := key("edge.example.com", model.DNSRecordTypeAAAA, "2001:0db8:0:0::1", "default")
	right := key("edge.example.com", model.DNSRecordTypeAAAA, "2001:db8::1", "default")
	if left != right {
		t.Fatalf("equivalent IPv6 record keys differ: %q != %q", left, right)
	}
}

func TestCoalescePendingClusterActionTracksLatestConfig(t *testing.T) {
	job := model.DNSSyncJob{Action: model.DNSSyncActionUPSERT_CLUSTER}

	action, siteID, changed := coalescePendingAction(job, nil, model.DNSSyncActionDELETE_CLUSTER)
	if !changed || action != model.DNSSyncActionDELETE_CLUSTER || siteID != nil {
		t.Fatalf("disable coalesce = (%q, %v, %v)", action, siteID, changed)
	}

	job.Action = action
	job.SiteId = siteID
	action, siteID, changed = coalescePendingAction(job, nil, model.DNSSyncActionUPSERT_CLUSTER)
	if !changed || action != model.DNSSyncActionUPSERT_CLUSTER || siteID != nil {
		t.Fatalf("re-enable coalesce = (%q, %v, %v)", action, siteID, changed)
	}

	job.Action = action
	job.SiteId = siteID
	if _, _, changed = coalescePendingAction(job, nil, model.DNSSyncActionUPSERT_CLUSTER); changed {
		t.Fatal("identical pending cluster action should be reused")
	}
}

func TestClusterActionSupersedesPendingSiteAction(t *testing.T) {
	siteID := "site-1"
	job := model.DNSSyncJob{Action: model.DNSSyncActionUPSERT_SITE, SiteId: &siteID}
	action, pendingSiteID, changed := coalescePendingAction(job, nil, model.DNSSyncActionDELETE_CLUSTER)
	if !changed || action != model.DNSSyncActionDELETE_CLUSTER || pendingSiteID != nil {
		t.Fatalf("cluster coalesce = (%q, %v, %v)", action, pendingSiteID, changed)
	}
}

func TestRefreshPendingJobSetsMakesLatestActionImmediatelyRunnable(t *testing.T) {
	now := time.Date(2026, 8, 13, 8, 0, 0, 0, time.UTC)
	sets := refreshPendingJobSets(model.DNSSyncActionDELETE_CLUSTER, nil, now)
	got := make(map[string]any, len(sets))
	for _, set := range sets {
		got[set.Field] = set.Value
	}
	want := map[string]any{
		"action":              model.DNSSyncActionDELETE_CLUSTER,
		"attempts":            0,
		"next_attempt_at":     now,
		"lease_owner":         nil,
		"lease_until":         nil,
		"heartbeat_at":        nil,
		"cancel_requested_at": nil,
		"timeout_at":          nil,
		"result_json":         nil,
		"compensation_json":   nil,
		"error":               nil,
		"updated_at":          now,
		"site_id":             nil,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("refreshPendingJobSets() = %#v; want %#v", got, want)
	}
}

func TestResolveClusterActionUsesLockedConfigState(t *testing.T) {
	tests := []struct {
		name       string
		requested  model.DNSSyncAction
		config     *model.DNSProviderConfig
		want       model.DNSSyncAction
		configured bool
	}{
		{
			name: "late upsert cannot overwrite disabled delete", requested: model.DNSSyncActionUPSERT_CLUSTER,
			config: &model.DNSProviderConfig{Enabled: false}, want: model.DNSSyncActionDELETE_CLUSTER, configured: true,
		},
		{
			name: "late delete cannot overwrite re-enabled upsert", requested: model.DNSSyncActionDELETE_CLUSTER,
			config: &model.DNSProviderConfig{Enabled: true}, want: model.DNSSyncActionUPSERT_CLUSTER, configured: true,
		},
		{
			name: "deleted config does not enqueue stale work", requested: model.DNSSyncActionUPSERT_CLUSTER,
		},
		{
			name: "site action is unchanged", requested: model.DNSSyncActionUPSERT_SITE,
			want: model.DNSSyncActionUPSERT_SITE, configured: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, configured := resolveClusterAction(test.requested, test.config)
			if got != test.want || configured != test.configured {
				t.Fatalf("resolveClusterAction() = (%q, %v); want (%q, %v)", got, configured, test.want, test.configured)
			}
		})
	}
}

func TestDroppedFromSelectionCountsNodesBeyondCandidateSet(t *testing.T) {
	selected := []NodeCandidate{candidate("a", 0, time.Now(), time.Now())}
	published := map[string]bool{"a": true, "b": true, "gone": true}
	got := droppedFromSelection(published, selected)
	want := []string{"b", "gone"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("droppedFromSelection() = %v, want %v", got, want)
	}
}

func TestPaceRemovalsDefersAndDrainsOverFollowUpPasses(t *testing.T) {
	service := &Service{}
	now := time.Now()
	policy := DefaultSchedulerPolicy()
	published := map[string]bool{}
	candidates := make([]NodeCandidate, 0, 10)
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("node-%02d", i)
		published[id] = true
		// Older UpdatedAt values are removed first when pacing defers.
		candidates = append(candidates, candidate(id, 0, now.Add(time.Duration(i)*time.Minute), now.Add(-time.Hour)))
	}
	selected := candidates[:6]

	// First pass: 4 dropped, at most floor(0.34*10)=3 may leave, so the
	// freshest dropped node stays published and a follow-up pass is required.
	result, deferred, err := service.paceRemovals(context.Background(), candidates, selected, published, policy, false)
	if err != nil {
		t.Fatal(err)
	}
	if !deferred {
		t.Fatal("pacing beyond the removal cap must report a deferral")
	}
	if got := selectedIDs(result); len(got) != 7 || got[6] != "node-09" {
		t.Fatalf("freshest dropped node must be deferred, got %v", got)
	}

	// The follow-up pass observes the three removals from the first pass and
	// drains the rest without another deferral.
	for _, id := range []string{"node-06", "node-07", "node-08"} {
		delete(published, id)
	}
	result, deferred, err = service.paceRemovals(context.Background(), candidates, selected, published, policy, false)
	if err != nil {
		t.Fatal(err)
	}
	if deferred {
		t.Fatalf("remaining removals must fit the cap, got %v", selectedIDs(result))
	}
	if got := selectedIDs(result); len(got) != 6 {
		t.Fatalf("deferred node must drain on the follow-up pass, got %v", got)
	}
}

func TestPaceRemovalsSkipsDeferralWhenNothingEligibleRemains(t *testing.T) {
	service := &Service{}
	published := map[string]bool{"a": true, "b": true}
	// No selected node: records pointing at dead nodes must leave at once.
	result, deferred, err := service.paceRemovals(context.Background(), nil, nil, published, SchedulerPolicy{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if deferred || len(result) != 0 {
		t.Fatalf("empty selection must bypass pacing, got %v deferred=%v", selectedIDs(result), deferred)
	}
}
