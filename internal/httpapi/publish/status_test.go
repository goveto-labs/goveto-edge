package publish

import "testing"

// TestClassifyClusterState pins the partial-failure aggregation rules. A
// cluster that is still syncing (pending/running tasks exist) must report
// "syncing" even when older tasks already failed, so an operator watching a
// rollback sees it converge instead of a stale "failed". Only once all active
// work settles does a lingering failure surface as "failed"; otherwise idle.
func TestClassifyClusterState(t *testing.T) {
	tests := []struct {
		name      string
		active    bool
		hasFailed bool
		want      string
	}{
		{name: "idle", active: false, hasFailed: false, want: "idle"},
		{name: "failed tasks but still syncing", active: true, hasFailed: true, want: "syncing"},
		{name: "syncing only", active: true, hasFailed: false, want: "syncing"},
		{name: "failed after settling", active: false, hasFailed: true, want: "failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := classifyClusterState(test.active, test.hasFailed); got != test.want {
				t.Fatalf("classifyClusterState(%v, %v) = %q, want %q", test.active, test.hasFailed, got, test.want)
			}
		})
	}
}
