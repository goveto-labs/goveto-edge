package agentbench

import (
	"testing"
	"time"
)

func TestPercentileUsesNearestRank(t *testing.T) {
	values := []time.Duration{40, 10, 30, 20}
	if got := Percentile(values, 50); got != 20 {
		t.Fatalf("p50=%s, want 20ns", got)
	}
	if got := Percentile(values, 99); got != 40 {
		t.Fatalf("p99=%s, want 40ns", got)
	}
	if values[0] != 40 {
		t.Fatal("Percentile mutated its input")
	}
}

func TestValidateRunsRejectsVariationFailuresAndLoadSaturation(t *testing.T) {
	runs := []Run{{Index: 1, Metrics: Metrics{RPS: 100}}, {Index: 2, Metrics: Metrics{RPS: 120, Failures: 1}}}
	validity := ValidateRuns(runs, 90)
	if validity.Valid {
		t.Fatal("invalid runs were accepted")
	}
	if len(validity.Reasons) != 3 {
		t.Fatalf("reasons=%v, want failure, variation, and CPU", validity.Reasons)
	}
}

func TestCompareAppliesArchitectureAndRegressionGates(t *testing.T) {
	baseline := Report{Platform: Platform{Architecture: "amd64"}, Scenario: Scenario{Name: "hit", Protocol: ProtocolH2}, Summary: Metrics{RPS: 1000, P99MS: 10}}
	current := baseline
	current.Summary = Metrics{RPS: 899, P99MS: 11.6}
	decision := Compare(current, baseline)
	if decision.Passed {
		t.Fatal("throughput and p99 regressions passed")
	}
	current.Platform.Architecture = "arm64"
	decision = Compare(current, baseline)
	if decision.Passed || decision.Reason == "" {
		t.Fatalf("cross-architecture comparison=%+v", decision)
	}
}
