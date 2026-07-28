package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"goveto-edge/internal/agentbench"
)

type headerFlags map[string]string

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
	suite := flags.String("suite", string(agentbench.SuitePR), "pr, nightly, capacity, or soak")
	protocol := flags.String("protocol", string(agentbench.ProtocolH1), "h1, h2, or h3")
	scenario := flags.String("scenario", "pure-origin-16k", "stable scenario name")
	targetURL := flags.String("url", "", "Edge Agent URL")
	host := flags.String("host", "", "HTTP Host and TLS server name")
	output := flags.String("output", "benchmark-results", "artifact output directory")
	baseline := flags.String("baseline", "", "baseline report.json")
	duration := flags.Duration("duration", 0, "measurement duration")
	warmup := flags.Duration("warmup", 0, "warmup duration")
	repeats := flags.Int("repeats", 0, "measurement repetitions")
	concurrency := flags.Int("concurrency", 32, "concurrent workers")
	timeout := flags.Duration("request-timeout", 10*time.Second, "per-request timeout")
	expectedStatus := flags.Int("expected-status", 200, "expected response status")
	expectedHash := flags.String("expected-sha256", "", "expected response body SHA-256")
	insecure := flags.Bool("insecure-skip-verify", false, "accept the benchmark environment's private certificate")
	newConnection := flags.Bool("new-connection", false, "create a new transport for every request")
	agentPID := flags.Int("agent-pid", 0, "Edge Agent PID to sample")
	agentMetricsURL := flags.String("agent-metrics-url", "", "optional benchmark telemetry URL")
	agentBinary := flags.String("agent-binary", "", "Edge Agent binary to hash")
	var expectedHeaders headerFlags
	flags.Var(&expectedHeaders, "expected-header", "required response header Name=Value (repeatable)")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *targetURL == "" {
		return errors.New("--url is required")
	}
	selectedSuite := agentbench.Suite(*suite)
	if !validSuite(selectedSuite) {
		return fmt.Errorf("invalid suite %q", *suite)
	}
	defaultWarmup, defaultDuration, defaultRepeats := suiteDefaults(selectedSuite)
	if *warmup == 0 {
		*warmup = defaultWarmup
	}
	if *duration == 0 {
		*duration = defaultDuration
	}
	if *repeats == 0 {
		*repeats = defaultRepeats
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	report, err := agentbench.RunBenchmark(ctx, agentbench.Config{
		Suite: selectedSuite, Protocol: agentbench.Protocol(*protocol), Scenario: *scenario, URL: *targetURL, Host: *host,
		Concurrency: *concurrency, Duration: *duration, Warmup: *warmup, Repeats: *repeats, RequestTimeout: *timeout,
		ExpectedStatus: *expectedStatus, ExpectedSHA256: strings.TrimSpace(*expectedHash), ExpectedHeaders: expectedHeaders,
		InsecureSkipVerify: *insecure, NewConnection: *newConnection, AgentPID: int32(*agentPID), AgentMetricsURL: *agentMetricsURL, SampleInterval: time.Second,
	})
	if err != nil {
		return err
	}
	report.Commit = agentbench.GitCommit()
	if *agentBinary != "" {
		report.BinarySHA256 = agentbench.FileSHA256(*agentBinary)
	}
	if *baseline != "" {
		baselineReport, readErr := agentbench.ReadReport(*baseline)
		if readErr != nil {
			return fmt.Errorf("read baseline: %w", readErr)
		}
		decision := agentbench.Compare(report, baselineReport)
		report.Baseline = &decision
	}
	if err := agentbench.WriteArtifacts(*output, report); err != nil {
		return fmt.Errorf("write artifacts: %w", err)
	}
	fmt.Printf("RPS %.2f, p99 %.2f ms, success %.4f%%; artifacts: %s\n", report.Summary.RPS, report.Summary.P99MS, report.Summary.SuccessRate*100, *output)
	if !report.Validity.Valid {
		return errors.New("benchmark result is invalid: " + strings.Join(report.Validity.Reasons, "; "))
	}
	if report.Baseline != nil && !report.Baseline.Passed {
		return errors.New("benchmark baseline gate failed")
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
		return 30 * time.Second, 120 * time.Second, 5
	case agentbench.SuiteSoak:
		return 30 * time.Second, 6 * time.Hour, 1
	default:
		return 0, 0, 0
	}
}

func validSuite(suite agentbench.Suite) bool {
	return suite == agentbench.SuitePR || suite == agentbench.SuiteNightly || suite == agentbench.SuiteCapacity || suite == agentbench.SuiteSoak
}
