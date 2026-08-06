package agentbench

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"time"
)

const (
	maxPostResetHeapGrowth = uint64(32 << 20)
	maxPostResetRSSGrowth  = uint64(64 << 20)
)

func Percentile(values []time.Duration, percentile float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	ordered := append([]time.Duration(nil), values...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	if percentile <= 0 {
		return ordered[0]
	}
	if percentile >= 100 {
		return ordered[len(ordered)-1]
	}
	index := int(math.Ceil(percentile/100*float64(len(ordered)))) - 1
	return ordered[max(index, 0)]
}

func CoefficientOfVariation(values []float64) float64 {
	if len(values) < 2 {
		return 0
	}
	var sum float64
	for _, value := range values {
		sum += value
	}
	mean := sum / float64(len(values))
	if mean == 0 {
		return 0
	}
	var squared float64
	for _, value := range values {
		delta := value - mean
		squared += delta * delta
	}
	return math.Sqrt(squared/float64(len(values))) / mean
}

func Summarize(runs []Run) Metrics {
	if len(runs) == 0 {
		return Metrics{}
	}
	metrics := append([]Run(nil), runs...)
	sort.Slice(metrics, func(i, j int) bool { return metrics[i].Metrics.RPS < metrics[j].Metrics.RPS })
	result := metrics[len(metrics)/2].Metrics
	result.NegotiatedProtocol = commonProtocol(runs)
	result.HTTPStatusCounts = aggregateStatusCounts(runs)
	return result
}

func ValidateRuns(runs []Run, loadCPUPercentMax, maxLoadCPUPercent float64) Validity {
	return validateRuns(runs, loadCPUPercentMax, maxLoadCPUPercent, false)
}

func validateRuns(runs []Run, loadCPUPercentMax, maxLoadCPUPercent float64, capacityProbe bool) Validity {
	validity := Validity{Valid: true, Status: ResultPass, LoadCPUPercentMax: loadCPUPercentMax}
	rps := make([]float64, 0, len(runs))
	for _, run := range runs {
		rps = append(rps, run.Metrics.RPS)
		if run.Metrics.Failures > 0 {
			status := ResultProductFail
			capacityFailures := capacityFailureCount(run.ErrorCounts)
			if capacityProbe && capacityFailures == run.Metrics.Failures {
				status = ResultTargetSaturated
			}
			validity.addReason(status, fmt.Sprintf("run %d had %d failed requests (%d capacity-shaped)", run.Index, run.Metrics.Failures, capacityFailures))
		}
	}
	validity.RPSCoefficientVar = CoefficientOfVariation(rps)
	if validity.RPSCoefficientVar > 0.05 {
		validity.addReason(ResultEnvInvalid, fmt.Sprintf("RPS coefficient of variation %.2f%% exceeds 5%%", validity.RPSCoefficientVar*100))
	}
	if maxLoadCPUPercent <= 0 {
		maxLoadCPUPercent = 85
	}
	if loadCPUPercentMax > maxLoadCPUPercent {
		validity.addReason(ResultLoadSaturated, fmt.Sprintf("load generator CPU %.2f%% exceeds %.2f%%", loadCPUPercentMax, maxLoadCPUPercent))
	}
	validity.Valid = len(validity.Reasons) == 0 || validity.Status == ResultTargetSaturated
	return validity
}

func ValidateResourceExpectations(validity Validity, runs []Run, config Config) Validity {
	if validity.Status == "" {
		validity.Status = ResultPass
	}
	for _, run := range runs {
		for _, cleanupErr := range run.CleanupErrors {
			validity.addReason(ResultProductFail, fmt.Sprintf("run %d cleanup failed: %s", run.Index, cleanupErr))
		}
		for _, environmentErr := range run.EnvironmentErrors {
			validity.addReason(ResultEnvInvalid, fmt.Sprintf("run %d benchmark instrumentation failed: %s", run.Index, environmentErr))
		}
		checks := []struct {
			name    string
			actual  uint64
			minimum uint64
		}{
			{"cache hits", run.Resources.CacheHitsDelta, config.MinCacheHits},
			{"cache misses", run.Resources.CacheMissesDelta, config.MinCacheMisses},
			{"cache evictions", run.Resources.CacheEvictionsDelta, config.MinCacheEvictions},
		}
		for _, check := range checks {
			if check.minimum > 0 && check.actual < check.minimum {
				validity.addReason(ResultProductFail, fmt.Sprintf("run %d had %d %s, want at least %d", run.Index, check.actual, check.name, check.minimum))
			}
		}
		if config.MaxCapturedValues > 0 {
			for name, values := range run.Metrics.ResponseHeaders {
				if len(values) > config.MaxCapturedValues {
					validity.addReason(ResultProductFail, fmt.Sprintf("run %d captured %d distinct %s values, want at most %d", run.Index, len(values), name, config.MaxCapturedValues))
				}
			}
		}
		for name, limits := range config.MaxHeaderRatios {
			values := run.Metrics.ResponseHeaders[http.CanonicalHeaderKey(name)]
			var total uint64
			for _, count := range values {
				total += count
			}
			for value, maximum := range limits {
				actual := 0.0
				if total > 0 {
					actual = float64(values[value]) / float64(total)
				}
				if total == 0 || actual > maximum {
					validity.addReason(ResultProductFail, fmt.Sprintf("run %d had %s=%q ratio %.4f, want at most %.4f", run.Index, name, value, actual, maximum))
				}
			}
		}
		for status, minimum := range config.MinStatusCounts {
			if actual := run.Metrics.HTTPStatusCounts[status]; actual < minimum {
				validity.addReason(ResultProductFail, fmt.Sprintf("run %d had %d HTTP %d responses, want at least %d", run.Index, actual, status, minimum))
			}
		}
		for status, maximum := range config.MaxStatusCounts {
			if actual := run.Metrics.HTTPStatusCounts[status]; actual > maximum {
				validity.addReason(ResultProductFail, fmt.Sprintf("run %d had %d HTTP %d responses, want at most %d", run.Index, actual, status, maximum))
			}
		}
		if config.MaxAgentRSSBytes > 0 && run.Resources.RSSBytesMax > config.MaxAgentRSSBytes {
			validity.addReason(ResultProductFail, fmt.Sprintf("run %d agent RSS peaked at %d bytes, want at most %d", run.Index, run.Resources.RSSBytesMax, config.MaxAgentRSSBytes))
		}
		if config.MaxAgentRSSGrowthBytes > 0 && run.Resources.RSSBytesGrowth > config.MaxAgentRSSGrowthBytes {
			validity.addReason(ResultProductFail, fmt.Sprintf("run %d agent RSS grew by %d bytes, want at most %d", run.Index, run.Resources.RSSBytesGrowth, config.MaxAgentRSSGrowthBytes))
		}
		if config.MaxAgentGoroutineGrowth > 0 && run.Resources.GoroutinesCooldownGrowth > config.MaxAgentGoroutineGrowth {
			validity.addReason(ResultProductFail, fmt.Sprintf("run %d agent goroutines grew by %d from pre-warmup to cooldown end, want at most %d", run.Index, run.Resources.GoroutinesCooldownGrowth, config.MaxAgentGoroutineGrowth))
		}
		if config.MinRPS > 0 && run.Metrics.RPS < config.MinRPS {
			validity.addReason(ResultProductFail, fmt.Sprintf("run %d RPS %.2f is below %.2f", run.Index, run.Metrics.RPS, config.MinRPS))
		}
		if config.MaxP99MS > 0 && run.Metrics.P99MS > config.MaxP99MS {
			validity.addReason(ResultProductFail, fmt.Sprintf("run %d p99 %.2f ms exceeds %.2f ms", run.Index, run.Metrics.P99MS, config.MaxP99MS))
		}
		if config.MaxAllocationBytesPerRequest > 0 && run.Resources.AllocationBytesPerRequest > float64(config.MaxAllocationBytesPerRequest) {
			validity.addReason(ResultProductFail, fmt.Sprintf("run %d allocated %.2f bytes/request, want at most %d", run.Index, run.Resources.AllocationBytesPerRequest, config.MaxAllocationBytesPerRequest))
		}
		if config.RequireCacheWritesDrained {
			if run.Resources.CacheWriteQueueDepthEnd != 0 || run.Resources.CacheWriteQueueBytesEnd != 0 || run.Resources.CacheInflightWritesEnd != 0 {
				validity.addReason(ResultProductFail, fmt.Sprintf("run %d cache writes did not drain (queue=%d/%d inflight=%d)", run.Index, run.Resources.CacheWriteQueueDepthEnd, run.Resources.CacheWriteQueueBytesEnd, run.Resources.CacheInflightWritesEnd))
			}
			if run.Resources.CacheWriteRejectionsDelta != 0 {
				validity.addReason(ResultProductFail, fmt.Sprintf("run %d rejected %d cache writes", run.Index, run.Resources.CacheWriteRejectionsDelta))
			}
		}
		if config.Cooldown > 0 {
			checkCooldownResources(&validity, run)
		}
		if config.PostCooldownCacheReset {
			checkPostResetResources(&validity, run)
		}
		if config.RequireCompleteAccessLogs {
			if run.Resources.MemoryDroppedLogsDelta != 0 || run.Resources.DiskDroppedLogsDelta != 0 {
				validity.addReason(ResultProductFail, fmt.Sprintf("run %d dropped access logs (memory=%d disk=%d)", run.Index, run.Resources.MemoryDroppedLogsDelta, run.Resources.DiskDroppedLogsDelta))
			}
			if run.Resources.QueueRecordsEnd != 0 || run.Resources.BufferRecordsEnd != 0 || run.Resources.QueueBytesEnd != 0 || run.Resources.BufferBytesEnd != 0 {
				validity.addReason(ResultProductFail, fmt.Sprintf("run %d access log queue did not drain (disk=%d/%d memory=%d/%d)", run.Index, run.Resources.QueueRecordsEnd, run.Resources.QueueBytesEnd, run.Resources.BufferRecordsEnd, run.Resources.BufferBytesEnd))
			}
			if run.Resources.CommittedRecordsDelta < run.Metrics.Successes {
				validity.addReason(ResultProductFail, fmt.Sprintf("run %d committed %d access logs for %d successful requests", run.Index, run.Resources.CommittedRecordsDelta, run.Metrics.Successes))
			}
		}
	}
	validity.Valid = len(validity.Reasons) == 0 || validity.Status == ResultTargetSaturated
	return validity
}

func checkPostResetResources(validity *Validity, run Run) {
	checks := []struct {
		name       string
		start, end uint64
		growth     uint64
	}{
		{name: "heap bytes", start: run.Resources.HeapBytesStart, end: run.Resources.HeapBytesPostResetGC, growth: maxPostResetHeapGrowth},
		{name: "RSS bytes", start: run.Resources.RSSBytesPreWarmup, end: run.Resources.RSSBytesPostResetGC, growth: maxPostResetRSSGrowth},
	}
	for _, check := range checks {
		if check.start > 0 && check.end > check.start+check.growth {
			validity.addReason(ResultProductFail, fmt.Sprintf("run %d post-reset %s ended at %d, baseline %d, limit %d", run.Index, check.name, check.end, check.start, check.start+check.growth))
		}
	}
}

func checkCooldownResources(validity *Validity, run Run) {
	rssEnd := run.Resources.RSSBytesEnd
	heapEnd := run.Resources.HeapBytesEnd
	if run.Resources.RSSBytesPostGC > 0 {
		rssEnd = run.Resources.RSSBytesPostGC
	}
	if run.Resources.HeapBytesPostGC > 0 {
		heapEnd = run.Resources.HeapBytesPostGC
	}
	checks := []struct {
		name     string
		start    uint64
		end      uint64
		additive uint64
	}{
		{"RSS", run.Resources.RSSBytesStart, rssEnd, 64 << 20},
		{"heap", run.Resources.HeapBytesStart, heapEnd, 32 << 20},
		{"file descriptors", uint64(max(run.Resources.FDsStart, 0)), uint64(max(run.Resources.FDsEnd, 0)), 32},
		{"goroutines", uint64(max(run.Resources.GoroutinesStart, 0)), uint64(max(run.Resources.GoroutinesEnd, 0)), 32},
	}
	for _, check := range checks {
		if check.start == 0 || check.end == 0 {
			continue
		}
		limit := max(uint64(float64(check.start)*1.2), check.start+check.additive)
		if check.end > limit {
			validity.addReason(ResultProductFail, fmt.Sprintf("run %d cooldown %s ended at %d, baseline %d, limit %d", run.Index, check.name, check.end, check.start, limit))
		}
	}
}

func (validity *Validity) addReason(status ResultStatus, reason string) {
	validity.Reasons = append(validity.Reasons, reason)
	if resultStatusPriority(status) > resultStatusPriority(validity.Status) {
		validity.Status = status
	}
}

func resultStatusPriority(status ResultStatus) int {
	switch status {
	case ResultProductFail:
		return 4
	case ResultEnvInvalid:
		return 3
	case ResultLoadSaturated:
		return 2
	case ResultTargetSaturated:
		return 1
	default:
		return 0
	}
}

func Compare(current, baseline Report) BaselineDecision {
	decision := BaselineDecision{Passed: true}
	if current.RunnerID == "" || current.RunnerID != baseline.RunnerID {
		decision.Passed = false
		decision.Reason = "baseline runner does not match current runner"
		return decision
	}
	if current.Platform.Architecture != baseline.Platform.Architecture {
		decision.Passed = false
		decision.Reason = "baseline architecture does not match current architecture"
		return decision
	}
	if current.Scenario.Suite != baseline.Scenario.Suite || current.Scenario.Name != baseline.Scenario.Name || current.Scenario.Protocol != baseline.Scenario.Protocol || current.Scenario.Variant != baseline.Scenario.Variant {
		decision.Passed = false
		decision.Reason = "baseline suite, scenario, protocol, or variant does not match"
		return decision
	}
	if current.Scenario.Concurrency != baseline.Scenario.Concurrency || current.Scenario.NewConnection != baseline.Scenario.NewConnection {
		decision.Passed = false
		decision.Reason = "baseline concurrency configuration does not match"
		return decision
	}
	decision.Comparisons = []MetricComparison{
		lowerIsBetter("p99_ms", current.Summary.P99MS, baseline.Summary.P99MS, 15),
		higherIsBetter("rps", current.Summary.RPS, baseline.Summary.RPS, 10),
	}
	for _, comparison := range decision.Comparisons {
		decision.Passed = decision.Passed && comparison.Passed
	}
	return decision
}

// ApplyBaselineRatioGates adds opt-in improvement targets to a compatible baseline decision.
func ApplyBaselineRatioGates(decision BaselineDecision, current, baseline Report, minRPSRatio, maxAllocationRatio float64) BaselineDecision {
	if decision.Reason != "" {
		return decision
	}
	if minRPSRatio > 0 {
		comparison := MetricComparison{
			Metric: "rps_ratio", Baseline: baseline.Summary.RPS, Current: current.Summary.RPS,
			ChangePercent: percentChange(current.Summary.RPS, baseline.Summary.RPS),
			LimitPercent:  (1 - minRPSRatio) * 100,
			Passed:        baseline.Summary.RPS == 0 || current.Summary.RPS >= baseline.Summary.RPS*minRPSRatio,
		}
		decision.Comparisons = append(decision.Comparisons, comparison)
		decision.Passed = decision.Passed && comparison.Passed
	}
	if maxAllocationRatio > 0 {
		baselineAllocation := medianAllocationBytesPerRequest(baseline.Runs)
		currentAllocation := medianAllocationBytesPerRequest(current.Runs)
		comparison := MetricComparison{
			Metric: "allocation_bytes_per_request_ratio", Baseline: baselineAllocation, Current: currentAllocation,
			ChangePercent: percentChange(currentAllocation, baselineAllocation),
			LimitPercent:  (maxAllocationRatio - 1) * 100,
			Passed:        baselineAllocation == 0 || currentAllocation <= baselineAllocation*maxAllocationRatio,
		}
		decision.Comparisons = append(decision.Comparisons, comparison)
		decision.Passed = decision.Passed && comparison.Passed
	}
	return decision
}

func medianAllocationBytesPerRequest(runs []Run) float64 {
	values := make([]float64, 0, len(runs))
	for _, run := range runs {
		values = append(values, run.Resources.AllocationBytesPerRequest)
	}
	if len(values) == 0 {
		return 0
	}
	sort.Float64s(values)
	return values[len(values)/2]
}

func CompareControl(full, control Report) ControlDecision {
	decision := ControlDecision{Passed: false, FullRPS: full.Summary.RPS, ControlRPS: control.Summary.RPS, MinimumRatio: 0.9}
	if full.Scenario.Variant != VariantFull || control.Scenario.Variant != VariantControl {
		decision.Reason = "control comparison requires full and control variants"
		return decision
	}
	if !full.Validity.Valid || !control.Validity.Valid {
		decision.Reason = "full and control reports must both be valid"
		return decision
	}
	if full.RunnerID != control.RunnerID || full.Platform.Architecture != control.Platform.Architecture ||
		full.Scenario.Suite != control.Scenario.Suite || full.Scenario.Name != control.Scenario.Name ||
		full.Scenario.Protocol != control.Scenario.Protocol || full.Scenario.Concurrency != control.Scenario.Concurrency ||
		full.Scenario.NewConnection != control.Scenario.NewConnection || full.Scenario.Method != control.Scenario.Method ||
		full.Scenario.URL != control.Scenario.URL || full.Scenario.DurationMS != control.Scenario.DurationMS ||
		full.Scenario.WarmupMS != control.Scenario.WarmupMS || full.Scenario.Repeats != control.Scenario.Repeats {
		decision.Reason = "control benchmark configuration does not match full benchmark"
		return decision
	}
	if control.Summary.RPS > 0 {
		decision.Ratio = full.Summary.RPS / control.Summary.RPS
	}
	decision.Passed = control.Summary.RPS > 0 && decision.Ratio >= decision.MinimumRatio && full.Summary.SuccessRate == 1 && control.Summary.SuccessRate == 1
	if !decision.Passed {
		decision.Reason = "full throughput must reach 90% of control with 100% success"
	}
	return decision
}

func aggregateStatusCounts(runs []Run) map[int]uint64 {
	counts := make(map[int]uint64)
	for _, run := range runs {
		for status, count := range run.Metrics.HTTPStatusCounts {
			counts[status] += count
		}
	}
	if len(counts) == 0 {
		return nil
	}
	return counts
}

func higherIsBetter(name string, current, baseline, limit float64) MetricComparison {
	change := percentChange(current, baseline)
	return MetricComparison{Metric: name, Baseline: baseline, Current: current, ChangePercent: change, LimitPercent: limit, Passed: baseline == 0 || change >= -limit}
}

func lowerIsBetter(name string, current, baseline, limit float64) MetricComparison {
	change := percentChange(current, baseline)
	return MetricComparison{Metric: name, Baseline: baseline, Current: current, ChangePercent: change, LimitPercent: limit, Passed: baseline == 0 || change <= limit}
}

func percentChange(current, baseline float64) float64 {
	if baseline == 0 {
		return 0
	}
	return (current - baseline) / baseline * 100
}

func commonProtocol(runs []Run) string {
	if len(runs) == 0 {
		return ""
	}
	protocol := runs[0].Metrics.NegotiatedProtocol
	for _, run := range runs[1:] {
		if run.Metrics.NegotiatedProtocol != protocol {
			return "mixed"
		}
	}
	return protocol
}
