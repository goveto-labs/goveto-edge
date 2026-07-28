package agentbench

import (
	"fmt"
	"math"
	"sort"
	"time"
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
	return result
}

func ValidateRuns(runs []Run, loadCPUPercentMax float64) Validity {
	validity := Validity{Valid: true, LoadCPUPercentMax: loadCPUPercentMax}
	rps := make([]float64, 0, len(runs))
	for _, run := range runs {
		rps = append(rps, run.Metrics.RPS)
		if run.Metrics.Failures > 0 {
			validity.Reasons = append(validity.Reasons, fmt.Sprintf("run %d had %d failed requests", run.Index, run.Metrics.Failures))
		}
	}
	validity.RPSCoefficientVar = CoefficientOfVariation(rps)
	if validity.RPSCoefficientVar > 0.05 {
		validity.Reasons = append(validity.Reasons, fmt.Sprintf("RPS coefficient of variation %.2f%% exceeds 5%%", validity.RPSCoefficientVar*100))
	}
	if loadCPUPercentMax > 85 {
		validity.Reasons = append(validity.Reasons, fmt.Sprintf("load generator CPU %.2f%% exceeds 85%%", loadCPUPercentMax))
	}
	validity.Valid = len(validity.Reasons) == 0
	return validity
}

func Compare(current, baseline Report) BaselineDecision {
	decision := BaselineDecision{Passed: true}
	if current.Platform.Architecture != baseline.Platform.Architecture {
		decision.Passed = false
		decision.Reason = "baseline architecture does not match current architecture"
		return decision
	}
	if current.Scenario.Name != baseline.Scenario.Name || current.Scenario.Protocol != baseline.Scenario.Protocol {
		decision.Passed = false
		decision.Reason = "baseline scenario or protocol does not match"
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
