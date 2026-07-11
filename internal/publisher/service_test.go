package publisher

import "testing"

func TestSuccessfulTargetsPartitionsResults(t *testing.T) {
	targets := []target{{NodeID: "a"}, {NodeID: "b"}, {NodeID: "c"}}
	results := []targetResult{{NodeID: "a", Success: true}, {NodeID: "b", Error: "timeout"}, {NodeID: "c", Success: true}}

	succeeded, failed := successfulTargets(results, targets)
	if len(succeeded) != 2 || succeeded[0].NodeID != "a" || succeeded[1].NodeID != "c" {
		t.Fatalf("unexpected successful targets: %#v", succeeded)
	}
	if len(failed) != 1 || failed[0].NodeID != "b" || failed[0].Error != "timeout" {
		t.Fatalf("unexpected failed results: %#v", failed)
	}
}

func TestAllSucceeded(t *testing.T) {
	tests := []struct {
		name    string
		results []targetResult
		want    bool
	}{
		{name: "empty rollback", want: true},
		{name: "complete", results: []targetResult{{Success: true}, {Success: true}}, want: true},
		{name: "partial", results: []targetResult{{Success: true}, {Success: false}}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := allSucceeded(test.results); got != test.want {
				t.Fatalf("allSucceeded() = %v, want %v", got, test.want)
			}
		})
	}
}
