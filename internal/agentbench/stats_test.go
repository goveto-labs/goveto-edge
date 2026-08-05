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
	runs := []Run{{Index: 1, Metrics: Metrics{Requests: 10, Successes: 9, Failures: 1, RPS: 10}, ErrorCounts: map[string]uint64{"timeout": 1}}}
	validity := validateRuns(runs, 10, 85, true)
	if !validity.Valid || validity.Status != ResultTargetSaturated {
		t.Fatalf("validity=%+v", validity)
	}
}

func TestValidateRunsDoesNotDowngradeProductFailureInCapacityProbe(t *testing.T) {
	runs := []Run{{Index: 1, Metrics: Metrics{Requests: 10, Successes: 9, Failures: 1, RPS: 10}, ErrorCounts: map[string]uint64{"header_mismatch": 1}}}
	validity := validateRuns(runs, 10, 85, true)
	if validity.Valid || validity.Status != ResultProductFail {
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

func TestValidateResourceExpectationsUsesPostGCAndRelativeRSS(t *testing.T) {
	run := Run{Index: 1, Resources: ResourceSummary{
		RSSBytesStart: 600 << 20, RSSBytesMax: 720 << 20, RSSBytesGrowth: 120 << 20,
		RSSBytesEnd: 700 << 20, RSSBytesPostGC: 620 << 20,
		HeapBytesStart: 100 << 20, HeapBytesEnd: 400 << 20, HeapBytesPostGC: 110 << 20,
		FDsStart: 20, FDsEnd: 20, GoroutinesStart: 20, GoroutinesEnd: 20,
	}}
	validity := ValidateResourceExpectations(Validity{Valid: true, Status: ResultPass}, []Run{run}, Config{Cooldown: time.Minute, MaxAgentRSSGrowthBytes: 128 << 20})
	if !validity.Valid {
		t.Fatalf("post-GC recovery and relative growth should pass: %+v", validity)
	}
	validity = ValidateResourceExpectations(Validity{Valid: true, Status: ResultPass}, []Run{run}, Config{MaxAgentRSSGrowthBytes: 64 << 20})
	if validity.Valid || validity.Status != ResultProductFail {
		t.Fatalf("RSS growth limit was not enforced: %+v", validity)
	}
}

func TestValidateResourceExpectationsEnforcesPreWarmupGoroutineGrowth(t *testing.T) {
	run := Run{Index: 1, Resources: ResourceSummary{
		GoroutinesPreWarmup: 100, GoroutinesEnd: 357, GoroutinesCooldownGrowth: 257,
	}}
	config := Config{MaxAgentGoroutineGrowth: 256}
	validity := ValidateResourceExpectations(Validity{Valid: true, Status: ResultPass}, []Run{run}, config)
	if validity.Valid || validity.Status != ResultProductFail {
		t.Fatalf("goroutine growth limit was not enforced: %+v", validity)
	}
	run.Resources.GoroutinesEnd = 356
	run.Resources.GoroutinesCooldownGrowth = 256
	validity = ValidateResourceExpectations(Validity{Valid: true, Status: ResultPass}, []Run{run}, config)
	if !validity.Valid {
		t.Fatalf("boundary-valid goroutine growth failed: %+v", validity)
	}
}

func TestValidateResourceExpectationsCleanupErrorsRemainProductFailures(t *testing.T) {
	run := Run{Index: 1, CleanupErrors: []string{"close QUIC: failed"}}
	validity := ValidateResourceExpectations(Validity{Valid: true, Status: ResultTargetSaturated}, []Run{run}, Config{})
	if validity.Valid || validity.Status != ResultProductFail {
		t.Fatalf("cleanup failure was downgraded: %+v", validity)
	}
}

func TestValidateResourceExpectations(t *testing.T) {
	validity := ValidateResourceExpectations(Validity{Valid: true}, []Run{{Index: 1, Metrics: Metrics{ResponseHeaders: map[string]map[string]uint64{"X-Origin-Requests": {"1": 10, "2": 1}}}, Resources: ResourceSummary{CacheHitsDelta: 10, CacheMissesDelta: 2}}}, Config{MinCacheHits: 1, MinCacheMisses: 1, MinCacheEvictions: 1, MaxCapturedValues: 1})
	if validity.Valid || len(validity.Reasons) != 2 {
		t.Fatalf("validity=%+v", validity)
	}
}

func TestValidateResourceExpectationsEnforcesStatusCountsPerRun(t *testing.T) {
	runs := []Run{
		{Index: 1, Metrics: Metrics{HTTPStatusCounts: map[int]uint64{200: 100, 429: 1}}},
		{Index: 2, Metrics: Metrics{HTTPStatusCounts: map[int]uint64{200: 201}}},
	}
	config := Config{MinStatusCounts: map[int]uint64{429: 1}, MaxStatusCounts: map[int]uint64{200: 200}}
	validity := ValidateResourceExpectations(Validity{Valid: true, Status: ResultPass}, runs, config)
	if validity.Valid || validity.Status != ResultProductFail || len(validity.Reasons) != 2 {
		t.Fatalf("validity=%+v", validity)
	}
	validity = ValidateResourceExpectations(Validity{Valid: true, Status: ResultPass}, runs[:1], config)
	if !validity.Valid {
		t.Fatalf("status constraints rejected boundary-valid run: %+v", validity)
	}
}

func TestSummarizeAggregatesHTTPStatuses(t *testing.T) {
	summary := Summarize([]Run{
		{Metrics: Metrics{RPS: 10, HTTPStatusCounts: map[int]uint64{200: 3, 429: 1}}},
		{Metrics: Metrics{RPS: 20, HTTPStatusCounts: map[int]uint64{200: 4, 429: 2}}},
	})
	if summary.HTTPStatusCounts[200] != 7 || summary.HTTPStatusCounts[429] != 3 {
		t.Fatalf("status counts=%v", summary.HTTPStatusCounts)
	}
}

func TestCompareAppliesArchitectureAndRegressionGates(t *testing.T) {
	baseline := Report{RunnerID: "agent4", Platform: Platform{Architecture: "amd64"}, Scenario: Scenario{Suite: SuiteCapacity, Name: "hit", Protocol: ProtocolH2, Concurrency: 32}, Summary: Metrics{RPS: 1000, P99MS: 10}}
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
	current = baseline
	current.RunnerID = "agent8"
	decision = Compare(current, baseline)
	if decision.Passed || decision.Reason != "baseline runner does not match current runner" {
		t.Fatalf("cross-runner comparison=%+v", decision)
	}
	current = baseline
	current.Scenario.Concurrency = 128
	decision = Compare(current, baseline)
	if decision.Passed || decision.Reason != "baseline concurrency configuration does not match" {
		t.Fatalf("cross-concurrency comparison=%+v", decision)
	}
}

func TestCompareControlRequiresNinetyPercentThroughput(t *testing.T) {
	control := Report{
		RunnerID: "agent8", Platform: Platform{Architecture: "amd64"},
		Scenario: Scenario{Suite: SuiteCapacity, Name: "small-reuse", Protocol: ProtocolH1, Concurrency: 32, Variant: VariantControl},
		Summary:  Metrics{RPS: 1000, SuccessRate: 1},
		Validity: Validity{Valid: true, Status: ResultPass},
	}
	full := control
	full.Scenario.Variant = VariantFull
	full.Summary = Metrics{RPS: 900, SuccessRate: 1}
	if decision := CompareControl(full, control); !decision.Passed || decision.Ratio != 0.9 {
		t.Fatalf("90%% control comparison=%#v", decision)
	}
	full.Summary.RPS = 899
	if decision := CompareControl(full, control); decision.Passed {
		t.Fatalf("sub-90%% control comparison passed: %#v", decision)
	}
}

func TestOptionalBaselineRatioGates(t *testing.T) {
	baseline := Report{RunnerID: "agent8", Platform: Platform{Architecture: "amd64"}, Scenario: Scenario{Suite: SuiteCapacity, Name: "cold", Protocol: ProtocolH1, Concurrency: 32}, Summary: Metrics{RPS: 20}, Runs: []Run{{Resources: ResourceSummary{AllocationBytesPerRequest: 1000}}}}
	current := baseline
	current.Summary.RPS = 199
	current.Runs = []Run{{Resources: ResourceSummary{AllocationBytesPerRequest: 701}}}
	decision := ApplyBaselineRatioGates(Compare(current, baseline), current, baseline, 10, 0.7)
	if decision.Passed {
		t.Fatalf("ratio gates passed below their boundaries: %+v", decision)
	}
	current.Summary.RPS = 200
	current.Runs[0].Resources.AllocationBytesPerRequest = 700
	decision = ApplyBaselineRatioGates(Compare(current, baseline), current, baseline, 10, 0.7)
	if !decision.Passed {
		t.Fatalf("ratio gate boundaries failed: %+v", decision)
	}
}

func TestCompleteAccessLogGateChecksDropsDrainAndCommits(t *testing.T) {
	config := Config{RequireCompleteAccessLogs: true}
	run := Run{Index: 1, Metrics: Metrics{Successes: 10}, Resources: ResourceSummary{CommittedRecordsDelta: 9, BufferRecordsEnd: 1, MemoryDroppedLogsDelta: 1}}
	validity := ValidateResourceExpectations(Validity{Valid: true, Status: ResultPass}, []Run{run}, config)
	if validity.Valid || validity.Status != ResultProductFail || len(validity.Reasons) != 3 {
		t.Fatalf("incomplete access logs were not rejected: %#v", validity)
	}
	run.Resources = ResourceSummary{CommittedRecordsDelta: 10}
	validity = ValidateResourceExpectations(Validity{Valid: true, Status: ResultPass}, []Run{run}, config)
	if !validity.Valid || len(validity.Reasons) != 0 {
		t.Fatalf("complete access logs were rejected: %#v", validity)
	}
}

func TestPerformanceAndCacheWriteGates(t *testing.T) {
	config := Config{MinRPS: 200, MaxP99MS: 1000, MaxAllocationBytesPerRequest: 120000, RequireCacheWritesDrained: true}
	run := Run{Index: 1, Metrics: Metrics{RPS: 199, P99MS: 1001}, Resources: ResourceSummary{
		AllocationBytesPerRequest: 120001, CacheWriteQueueDepthEnd: 1,
		CacheWriteQueueBytesEnd: 2, CacheInflightWritesEnd: 1, CacheWriteRejectionsDelta: 1,
	}}
	validity := ValidateResourceExpectations(Validity{Valid: true, Status: ResultPass}, []Run{run}, config)
	if validity.Valid || validity.Status != ResultProductFail || len(validity.Reasons) != 5 {
		t.Fatalf("performance gates=%+v", validity)
	}
	run.Metrics.RPS = 200
	run.Metrics.P99MS = 1000
	run.Resources = ResourceSummary{AllocationBytesPerRequest: 120000}
	validity = ValidateResourceExpectations(Validity{Valid: true, Status: ResultPass}, []Run{run}, config)
	if !validity.Valid {
		t.Fatalf("boundary-valid performance result failed: %+v", validity)
	}
}

func TestApplyNaturalEndDoesNotEraseTelemetryAfterFailedCapture(t *testing.T) {
	summary := ResourceSummary{
		QueueBytesEnd: 11, TotalAllocBytes: 100, AllocationBytesPerRequest: 25,
		CacheWriteQueueDepthEnd: 2, CacheWriteQueueDepthMax: 3,
	}
	applyNaturalEnd(&summary, TimeSeriesPoint{Requests: 4})
	if summary.QueueBytesEnd != 11 || summary.TotalAllocBytes != 100 || summary.AllocationBytesPerRequest != 25 ||
		summary.CacheWriteQueueDepthEnd != 2 || summary.CacheWriteQueueDepthMax != 3 {
		t.Fatalf("failed telemetry capture erased the prior summary: %+v", summary)
	}

	summary.totalAllocStart = 100
	applyNaturalEnd(&summary, TimeSeriesPoint{
		Requests: 4, TotalAllocBytes: 200, CacheWriteQueueDepthMax: 5, telemetryCaptured: true,
	})
	if summary.AllocatedBytes != 100 || summary.AllocationBytesPerRequest != 25 || summary.CacheWriteQueueDepthMax != 5 {
		t.Fatalf("successful telemetry capture was not applied: %+v", summary)
	}
}
