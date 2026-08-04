package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"goveto-edge/internal/agentbench"
)

type headerFlags map[string]string

type multiHeaderFlags map[string][]string

type headerRatioFlags map[string]map[string]float64

type stringFlags []string

type statusFlags []int

type statusCountFlags map[int]uint64

func (values *stringFlags) String() string { return strings.Join(*values, ",") }
func (values *stringFlags) Set(input string) error {
	input = strings.TrimSpace(input)
	if input == "" {
		return errors.New("value cannot be empty")
	}
	*values = append(*values, input)
	return nil
}

func (values *statusFlags) String() string { return fmt.Sprint([]int(*values)) }
func (values *statusFlags) Set(input string) error {
	status, err := strconv.Atoi(strings.TrimSpace(input))
	if err != nil || status < 100 || status > 599 {
		return errors.New("status must be an integer between 100 and 599")
	}
	*values = append(*values, status)
	return nil
}

func (values *statusCountFlags) String() string { return fmt.Sprint(map[int]uint64(*values)) }
func (values *statusCountFlags) Set(input string) error {
	rawStatus, rawCount, ok := strings.Cut(input, "=")
	status, statusErr := strconv.Atoi(strings.TrimSpace(rawStatus))
	count, countErr := strconv.ParseUint(strings.TrimSpace(rawCount), 10, 64)
	if !ok || statusErr != nil || countErr != nil || status < 100 || status > 599 {
		return errors.New("status count must use STATUS=COUNT with STATUS between 100 and 599")
	}
	if *values == nil {
		*values = make(map[int]uint64)
	}
	(*values)[status] = count
	return nil
}

func (values *headerFlags) String() string {
	parts := make([]string, 0, len(*values))
	for name, value := range *values {
		parts = append(parts, name+"="+value)
	}
	return strings.Join(parts, ",")
}

func (values *headerFlags) Set(input string) error {
	name, value, ok := strings.Cut(input, "=")
	if !ok || strings.TrimSpace(name) == "" {
		return errors.New("header must use Name=Value")
	}
	if *values == nil {
		*values = make(map[string]string)
	}
	(*values)[strings.TrimSpace(name)] = value
	return nil
}

func (values *multiHeaderFlags) String() string { return fmt.Sprint(map[string][]string(*values)) }

func (values *multiHeaderFlags) Set(input string) error {
	name, value, ok := strings.Cut(input, "=")
	name = strings.TrimSpace(name)
	if !ok || name == "" || value == "" {
		return errors.New("header must use Name=Value")
	}
	if *values == nil {
		*values = make(map[string][]string)
	}
	(*values)[name] = append((*values)[name], value)
	return nil
}

func (values *headerRatioFlags) String() string {
	return fmt.Sprint(map[string]map[string]float64(*values))
}

func (values *headerRatioFlags) Set(input string) error {
	header, rawRatio, ok := strings.Cut(input, ":")
	name, value, hasHeader := strings.Cut(header, "=")
	ratio, err := strconv.ParseFloat(rawRatio, 64)
	if !ok || !hasHeader || strings.TrimSpace(name) == "" || value == "" || err != nil || ratio < 0 || ratio > 1 {
		return errors.New("header ratio must use Name=Value:Ratio with Ratio between 0 and 1")
	}
	if *values == nil {
		*values = make(map[string]map[string]float64)
	}
	name = strings.TrimSpace(name)
	if (*values)[name] == nil {
		(*values)[name] = make(map[string]float64)
	}
	(*values)[name][value] = ratio
	return nil
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 || args[0] != "run" {
		return errors.New("usage: agent-bench run [flags]")
	}
	flags := flag.NewFlagSet("agent-bench run", flag.ContinueOnError)
	runnerID := flags.String("runner-id", "default", "stable benchmark runner identifier")
	suite := flags.String("suite", string(agentbench.SuitePR), "pr, nightly, capacity, or soak")
	protocol := flags.String("protocol", string(agentbench.ProtocolH1), "h1, h2, or h3")
	scenario := flags.String("scenario", "pure-origin-16k", "stable scenario name")
	method := flags.String("method", "GET", "HTTP request method")
	targetURL := flags.String("url", "", "Edge Agent URL")
	host := flags.String("host", "", "HTTP Host and TLS server name")
	output := flags.String("output", "benchmark-results", "artifact output directory")
	baseline := flags.String("baseline", "", "baseline report.json")
	minBaselineRPSRatio := flags.Float64("min-baseline-rps-ratio", 0, "optional minimum current/baseline RPS ratio")
	maxBaselineAllocationRatio := flags.Float64("max-baseline-allocation-ratio", 0, "optional maximum current/baseline allocation ratio")
	control := flags.String("control", "", "matching control-variant report.json")
	variant := flags.String("variant", string(agentbench.VariantFull), "benchmark variant: full or control")
	requireCompleteAccessLogs := flags.Bool("require-complete-access-logs", false, "require zero access log loss and a drained queue")
	duration := flags.Duration("duration", 0, "measurement duration")
	warmup := flags.Duration("warmup", 0, "warmup duration")
	skipWarmup := flags.Bool("skip-warmup", false, "disable warmup even when the suite has a default")
	repeats := flags.Int("repeats", 0, "measurement repetitions")
	concurrency := flags.Int("concurrency", 32, "concurrent workers")
	timeout := flags.Duration("request-timeout", 10*time.Second, "per-request timeout")
	expectedStatus := flags.Int("expected-status", 200, "expected response status")
	expectedHash := flags.String("expected-sha256", "", "expected response body SHA-256")
	insecure := flags.Bool("insecure-skip-verify", false, "accept the benchmark environment's private certificate")
	newConnection := flags.Bool("new-connection", false, "create a new transport for every request")
	uniqueQuery := flags.Bool("unique-query", false, "append a unique _bench query value to every request")
	uniqueQueryCardinality := flags.Int("unique-query-cardinality", 0, "reuse this many numbered _bench query values instead of an unbounded sequence")
	uniqueQueryNamespace := flags.String("unique-query-namespace", "", "stable _bench prefix; each repeat gets a separate suffix")
	cooldown := flags.Duration("cooldown", 0, "continue resource sampling after load stops")
	capacityProbe := flags.Bool("capacity-probe", false, "classify request failures as target saturation instead of a compatibility failure")
	agentPID := flags.Int("agent-pid", 0, "Edge Agent PID to sample")
	agentMetricsURL := flags.String("agent-metrics-url", "", "optional benchmark telemetry URL")
	agentGCURL := flags.String("agent-gc-url", "", "optional benchmark-only post-cooldown GC URL")
	minCacheHits := flags.Uint64("min-cache-hits", 0, "minimum cache hit delta required in every run")
	minCacheMisses := flags.Uint64("min-cache-misses", 0, "minimum cache miss delta required in every run")
	minCacheEvictions := flags.Uint64("min-cache-evictions", 0, "minimum cache eviction delta required in every run")
	maxCapturedValues := flags.Int("max-captured-values", 0, "maximum distinct values allowed for each captured response header")
	agentBinary := flags.String("agent-binary", "", "Edge Agent binary to hash")
	maxLoadCPU := flags.Float64("max-load-cpu", 85, "maximum load generator CPU percent before marking the result saturated")
	maxAgentRSS := flags.Uint64("max-agent-rss", 0, "maximum Edge Agent RSS bytes allowed during the run")
	maxAgentRSSGrowth := flags.Uint64("max-agent-rss-growth", 0, "maximum Edge Agent RSS growth from the run baseline")
	minRPS := flags.Float64("min-rps", 0, "minimum requests per second required in every run")
	maxP99 := flags.Float64("max-p99", 0, "maximum p99 latency in milliseconds allowed in every run")
	maxAllocationBytesPerRequest := flags.Uint64("max-allocation-bytes-per-request", 0, "maximum allocated bytes per request")
	requireCacheWritesDrained := flags.Bool("require-cache-writes-drained", false, "drain cache writes and require an empty write queue with no rejections")
	var expectedHeaders headerFlags
	var allowedHeaders multiHeaderFlags
	var maxHeaderRatios headerRatioFlags
	var requestHeaders headerFlags
	var captureHeaders stringFlags
	var allowedStatuses statusFlags
	var minStatusCounts statusCountFlags
	var maxStatusCounts statusCountFlags
	flags.Var(&expectedHeaders, "expected-header", "required response header Name=Value (repeatable)")
	flags.Var(&allowedHeaders, "allowed-header", "allowed response header Name=Value (repeat a name for multiple values)")
	flags.Var(&maxHeaderRatios, "max-header-ratio", "maximum response header value ratio Name=Value:Ratio")
	flags.Var(&requestHeaders, "header", "request header Name=Value (repeatable)")
	flags.Var(&captureHeaders, "capture-header", "response header to count in the report (repeatable)")
	flags.Var(&allowedStatuses, "allowed-status", "allowed HTTP response status (repeatable; replaces expected-status matching)")
	flags.Var(&minStatusCounts, "min-status-count", "minimum response count per run as STATUS=COUNT (repeatable)")
	flags.Var(&maxStatusCounts, "max-status-count", "maximum response count per run as STATUS=COUNT (repeatable)")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *targetURL == "" {
		return errors.New("--url is required")
	}
	if *minBaselineRPSRatio < 0 || *maxBaselineAllocationRatio < 0 {
		return errors.New("baseline ratios cannot be negative")
	}
	selectedSuite := agentbench.Suite(*suite)
	if !validSuite(selectedSuite) {
		return fmt.Errorf("invalid suite %q", *suite)
	}
	defaultWarmup, defaultDuration, defaultRepeats := suiteDefaults(selectedSuite)
	if *warmup == 0 {
		*warmup = defaultWarmup
	}
	if *skipWarmup {
		*warmup = 0
	}
	if *duration == 0 {
		*duration = defaultDuration
	}
	if *repeats == 0 {
		*repeats = defaultRepeats
	}
	var baselineReport *agentbench.Report
	if *baseline != "" {
		loaded, readErr := agentbench.ReadReport(*baseline)
		if readErr != nil {
			return fmt.Errorf("read baseline: %w", readErr)
		}
		baselineReport = &loaded
	}
	var controlReport *agentbench.Report
	if *control != "" {
		loaded, readErr := agentbench.ReadReport(*control)
		if readErr != nil {
			return fmt.Errorf("read control report: %w", readErr)
		}
		controlReport = &loaded
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	report, err := agentbench.RunBenchmark(ctx, agentbench.Config{
		RunnerID: *runnerID, Suite: selectedSuite, Protocol: agentbench.Protocol(*protocol), Scenario: *scenario, Method: *method, URL: *targetURL, Host: *host,
		Concurrency: *concurrency, Duration: *duration, Warmup: *warmup, Repeats: *repeats, RequestTimeout: *timeout,
		ExpectedStatus: *expectedStatus, AllowedStatuses: allowedStatuses, MinStatusCounts: minStatusCounts, MaxStatusCounts: maxStatusCounts,
		ExpectedSHA256: strings.TrimSpace(*expectedHash), ExpectedHeaders: expectedHeaders,
		AllowedHeaders: allowedHeaders, MaxHeaderRatios: maxHeaderRatios,
		RequestHeaders: requestHeaders, CaptureHeaders: captureHeaders, InsecureSkipVerify: *insecure, NewConnection: *newConnection, UniqueQuery: *uniqueQuery,
		UniqueQueryCardinality: *uniqueQueryCardinality, UniqueQueryNamespace: strings.TrimSpace(*uniqueQueryNamespace),
		Cooldown: *cooldown, CapacityProbe: *capacityProbe,
		AgentPID: int32(*agentPID), AgentMetricsURL: *agentMetricsURL, AgentGCURL: *agentGCURL, SampleInterval: time.Second,
		MinCacheHits: *minCacheHits, MinCacheMisses: *minCacheMisses, MinCacheEvictions: *minCacheEvictions,
		MaxCapturedValues:      *maxCapturedValues,
		MaxLoadCPUPercent:      *maxLoadCPU,
		MaxAgentRSSBytes:       *maxAgentRSS,
		MaxAgentRSSGrowthBytes: *maxAgentRSSGrowth,
		MinRPS:                 *minRPS, MaxP99MS: *maxP99, MaxAllocationBytesPerRequest: *maxAllocationBytesPerRequest,
		Variant: agentbench.Variant(*variant), RequireCompleteAccessLogs: *requireCompleteAccessLogs,
		RequireCacheWritesDrained: *requireCacheWritesDrained,
	})
	if err != nil {
		return err
	}
	report.Commit = agentbench.GitCommit()
	if *agentBinary != "" {
		report.BinarySHA256 = agentbench.FileSHA256(*agentBinary)
	}
	if baselineReport != nil {
		decision := agentbench.Compare(report, *baselineReport)
		decision = agentbench.ApplyBaselineRatioGates(decision, report, *baselineReport, *minBaselineRPSRatio, *maxBaselineAllocationRatio)
		report.Baseline = &decision
	}
	if controlReport != nil {
		decision := agentbench.CompareControl(report, *controlReport)
		report.Control = &decision
		if !decision.Passed && report.Validity.Valid {
			report.Validity.Valid = false
			report.Validity.Status = agentbench.ResultProductFail
			report.Validity.Reasons = append(report.Validity.Reasons, "control gate failed: "+decision.Reason)
		}
	}
	if err := agentbench.WriteArtifacts(*output, report); err != nil {
		return fmt.Errorf("write artifacts: %w", err)
	}
	fmt.Printf("Status %s, RPS %.2f, p99 %.2f ms, success %.4f%%; artifacts: %s\n", report.Validity.Status, report.Summary.RPS, report.Summary.P99MS, report.Summary.SuccessRate*100, *output)
	if !report.Validity.Valid {
		return errors.New("benchmark result is invalid: " + strings.Join(report.Validity.Reasons, "; "))
	}
	if report.Baseline != nil && !report.Baseline.Passed {
		return errors.New("benchmark baseline gate failed")
	}
	if report.Control != nil && !report.Control.Passed {
		return errors.New("benchmark control gate failed: " + report.Control.Reason)
	}
	return nil
}

func suiteDefaults(suite agentbench.Suite) (time.Duration, time.Duration, int) {
	switch suite {
	case agentbench.SuitePR:
		return 2 * time.Second, 10 * time.Second, 3
	case agentbench.SuiteNightly:
		return 10 * time.Second, 30 * time.Second, 3
	case agentbench.SuiteCapacity:
		return 30 * time.Second, 120 * time.Second, 3
	case agentbench.SuiteSoak:
		return 30 * time.Second, 6 * time.Hour, 1
	default:
		return 0, 0, 0
	}
}

func validSuite(suite agentbench.Suite) bool {
	return suite == agentbench.SuitePR || suite == agentbench.SuiteNightly || suite == agentbench.SuiteCapacity || suite == agentbench.SuiteSoak
}
