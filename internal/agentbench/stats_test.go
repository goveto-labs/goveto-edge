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
	validity := ValidateRuns(runs, 90, 85)
	if validity.Valid {
		t.Fatal("invalid runs were accepted")
	}
	if len(validity.Reasons) != 3 {
		t.Fatalf("reasons=%v, want failure, variation, and CPU", validity.Reasons)
	}
	if validity.Status != ResultProductFail {
		t.Fatalf("status=%s", validity.Status)
	}
}

func TestValidateRunsSeparatesLoadSaturationFromProductFailure(t *testing.T) {
	validity := ValidateRuns([]Run{{Index: 1, Metrics: Metrics{RPS: 100}}}, 90, 85)
	if validity.Valid || validity.Status != ResultLoadSaturated {
		t.Fatalf("validity=%+v", validity)
	}
}

func TestValidateRunsClassifiesCapacityProbeFailureAsTargetSaturation(t *testing.T) {
	runs := []Run{{Index: 1, Metrics: Metrics{Requests: 10, Successes: 9, Failures: 1, RPS: 10}}}
	validity := validateRuns(runs, 10, 85, true)
	if !validity.Valid || validity.Status != ResultTargetSaturated {
		t.Fatalf("validity=%+v", validity)
	}
}

func TestValidateResourceExpectationsEnforcesHeaderValueRatio(t *testing.T) {
	runs := []Run{{Index: 1, Metrics: Metrics{ResponseHeaders: map[string]map[string]uint64{
		"X-Cache": {"HIT": 98, "STALE": 2},
	}}}}
	config := Config{MaxHeaderRatios: map[string]map[string]float64{"x-cache": {"STALE": 0.01}}}
	validity := ValidateResourceExpectations(Validity{Valid: true, Status: ResultPass}, runs, config)
	if validity.Valid || validity.Status != ResultProductFail {
		t.Fatalf("validity=%+v", validity)
	}
	runs[0].Metrics.ResponseHeaders["X-Cache"]["STALE"] = 1
	runs[0].Metrics.ResponseHeaders["X-Cache"]["HIT"] = 99
	validity = ValidateResourceExpectations(Validity{Valid: true, Status: ResultPass}, runs, config)
	if !validity.Valid {
		t.Fatalf("one percent stale should pass: %+v", validity)
	}
}

func TestValidateResourceExpectationsEnforcesRSSAndCooldownRecovery(t *testing.T) {
	run := Run{Index: 1, Resources: ResourceSummary{
		RSSBytesStart: 100 << 20, RSSBytesEnd: 220 << 20, RSSBytesMax: 600 << 20,
		HeapBytesStart: 50 << 20, HeapBytesEnd: 120 << 20,
		FDsStart: 20, FDsEnd: 80, GoroutinesStart: 20, GoroutinesEnd: 80,
	}}
	validity := ValidateResourceExpectations(Validity{Valid: true, Status: ResultPass}, []Run{run}, Config{Cooldown: time.Minute, MaxAgentRSSBytes: 512 << 20})
	if validity.Valid || validity.Status != ResultProductFail || len(validity.Reasons) != 5 {
		t.Fatalf("validity=%+v", validity)
	}
}

func TestValidateResourceExpectations(t *testing.T) {
	validity := ValidateResourceExpectations(Validity{Valid: true}, []Run{{Index: 1, Metrics: Metrics{ResponseHeaders: map[string]map[string]uint64{"X-Origin-Requests": {"1": 10, "2": 1}}}, Resources: ResourceSummary{CacheHitsDelta: 10, CacheMissesDelta: 2}}}, Config{MinCacheHits: 1, MinCacheMisses: 1, MinCacheEvictions: 1, MaxCapturedValues: 1})
	if validity.Valid || len(validity.Reasons) != 2 {
		t.Fatalf("validity=%+v", validity)
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
