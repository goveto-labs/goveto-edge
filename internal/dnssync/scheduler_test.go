package dnssync

import (
	"testing"
	"time"

	"goveto-edge/internal/storage/gen/model"
)

func candidate(id string, priority int, updatedAt, onlineSince time.Time) NodeCandidate {
	node := model.Node{Id: id, Status: model.NodeStatusONLINE, UpdatedAt: updatedAt}
	if !onlineSince.IsZero() {
		node.OnlineSince = &onlineSince
	}
	node.DnsPriority = priority
	return NodeCandidate{Node: node}
}

func selectedIDs(selected []NodeCandidate) []string {
	ids := make([]string, 0, len(selected))
	for _, item := range selected {
		ids = append(ids, item.Node.Id)
	}
	return ids
}

func TestSelectDefaultsToAllCandidates(t *testing.T) {
	now := time.Now()
	candidates := []NodeCandidate{
		candidate("a", 0, now, now.Add(-time.Hour)),
		candidate("b", 5, now, now.Add(-time.Hour)),
	}
	selected := Select(candidates, nil, SchedulerPolicy{}, now)
	if len(selected) != 2 {
		t.Fatalf("default policy must publish every candidate, got %v", selectedIDs(selected))
	}
}

func TestSelectHoldsBackNodeWithoutMinimumHealthyTime(t *testing.T) {
	now := time.Now()
	fresh := now.Add(-30 * time.Second)
	candidates := []NodeCandidate{
		candidate("fresh", 0, now, fresh),
		candidate("settled", 0, now, now.Add(-time.Hour)),
	}
	selected := Select(candidates, nil, SchedulerPolicy{}, now)
	if got := selectedIDs(selected); len(got) != 1 || got[0] != "settled" {
		t.Fatalf("freshly online node must be held back, got %v", got)
	}
	// Once published, a node is never held back again (sticky admission).
	published := map[string]bool{"fresh": true}
	selected = Select(candidates, published, SchedulerPolicy{}, now)
	if len(selected) != 2 {
		t.Fatalf("published node must stay published, got %v", selectedIDs(selected))
	}
}

func TestSelectAdmitsNodeWithoutOnlineSince(t *testing.T) {
	now := time.Now()
	candidates := []NodeCandidate{candidate("legacy", 0, now, time.Time{})}
	selected := Select(candidates, nil, SchedulerPolicy{MinHealthyTime: durationPointer(time.Hour)}, now)
	if len(selected) != 1 {
		t.Fatal("missing online_since must not lock a node out of DNS after upgrades")
	}
}

func TestSelectAdmitsFreshNodeWhenMinHealthyTimeDisabled(t *testing.T) {
	now := time.Now()
	candidates := []NodeCandidate{candidate("fresh", 0, now, now)}
	// An explicit zero duration disables the admission guard entirely.
	selected := Select(candidates, nil, SchedulerPolicy{MinHealthyTime: durationPointer(0)}, now)
	if len(selected) != 1 {
		t.Fatal("explicit zero MinHealthyTime must admit freshly online nodes")
	}
}

func TestSelectPrimaryBackupExpandsTiersUntilMinimum(t *testing.T) {
	now := time.Now()
	online := now.Add(-time.Hour)
	primary := candidate("primary", 0, now, online)
	backup1 := candidate("backup-1", 10, now, online)
	backup2 := candidate("backup-2", 20, now, online)

	selected := Select([]NodeCandidate{primary, backup1, backup2}, nil,
		SchedulerPolicy{Placement: model.DNSPlacementPRIMARY_BACKUP, MinPublished: 2}, now)
	if got := selectedIDs(selected); len(got) != 2 || got[0] != "primary" || got[1] != "backup-1" {
		t.Fatalf("primary tier plus first backup tier expected, got %v", got)
	}

	selected = Select([]NodeCandidate{primary, backup1, backup2}, nil,
		SchedulerPolicy{Placement: model.DNSPlacementPRIMARY_BACKUP, MinPublished: 1}, now)
	if got := selectedIDs(selected); len(got) != 1 || got[0] != "primary" {
		t.Fatalf("satisfied minimum must not pull in backups, got %v", got)
	}

	selected = Select([]NodeCandidate{backup1, backup2}, nil,
		SchedulerPolicy{Placement: model.DNSPlacementPRIMARY_BACKUP, MinPublished: 2}, now)
	if got := selectedIDs(selected); len(got) != 2 || got[0] != "backup-1" || got[1] != "backup-2" {
		t.Fatalf("lowest tier must be expanded first when no primary exists, got %v", got)
	}
}

func TestSchedulerPolicyWithDefaults(t *testing.T) {
	policy := SchedulerPolicy{Placement: model.DNSPlacementPRIMARY_BACKUP}.WithDefaults()
	if policy.MinPublished != 2 || policy.MinHealthyTime == nil || *policy.MinHealthyTime != 2*time.Minute || policy.MaxRemovalRatio != 0.34 {
		t.Fatalf("unset fields must fall back to defaults, got %+v", policy)
	}
	if (SchedulerPolicy{}).WithDefaults().Placement != model.DNSPlacementALL {
		t.Fatal("empty policy must default to ALL placement")
	}
	// An explicit zero MinHealthyTime survives WithDefaults: zero disables
	// the admission guard instead of selecting the default.
	if got := (SchedulerPolicy{MinHealthyTime: durationPointer(0)}).WithDefaults().MinHealthyTime; got == nil || *got != 0 {
		t.Fatalf("explicit zero MinHealthyTime must be preserved, got %v", got)
	}
}

func TestAllowedRemovalsAlwaysMakesProgress(t *testing.T) {
	if got := allowedRemovals(10, 0.34); got != 3 {
		t.Fatalf("34%% of 10 published nodes = 3, got %d", got)
	}
	if got := allowedRemovals(2, 0.34); got != 1 {
		t.Fatalf("floor must never block removal progress, got %d", got)
	}
	if got := allowedRemovals(1, 0.34); got != 1 {
		t.Fatalf("single node cluster must still drain, got %d", got)
	}
}

func TestDeferredRemovalsKeepsFreshestNodes(t *testing.T) {
	old := model.Node{Id: "old", UpdatedAt: time.Now().Add(-2 * time.Hour)}
	mid := model.Node{Id: "mid", UpdatedAt: time.Now().Add(-1 * time.Hour)}
	fresh := model.Node{Id: "fresh", UpdatedAt: time.Now()}
	deferred := deferredRemovals([]model.Node{old, mid, fresh}, 1)
	if len(deferred) != 1 || deferred[0].Id != "fresh" {
		t.Fatalf("freshest dropped node must stay published, got %v", deferred)
	}
	if got := deferredRemovals([]model.Node{old, mid}, 5); len(got) != 2 {
		t.Fatalf("overlarge keep must return everything, got %d", len(got))
	}
	if got := deferredRemovals([]model.Node{old}, 0); got != nil {
		t.Fatalf("zero keep must drop everything, got %v", got)
	}
}
