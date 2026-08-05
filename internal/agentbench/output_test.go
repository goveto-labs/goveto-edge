package agentbench

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteArtifactsAndReadReport(t *testing.T) {
	directory := t.TempDir()
	report := Report{SchemaVersion: SchemaVersion, RunnerID: "agent4", Platform: Platform{Architecture: "amd64"}, Scenario: Scenario{Name: "origin", Protocol: ProtocolH1, UniqueQueryNamespace: "attempt-2", MaxAgentGoroutineGrowth: 256}, Summary: Metrics{ResponseHeaders: map[string]map[string]uint64{"X-Cache": {"HIT": 10}}, HTTPStatusCounts: map[int]uint64{200: 8, 502: 2}}, Validity: Validity{Valid: false, Status: ResultProductFail, Reasons: []string{"request failed"}}, ErrorCounts: map[string]uint64{"http_5xx": 2}, Runs: []Run{{Index: 1, Resources: ResourceSummary{CacheHitsDelta: 10, RSSBytesPreWarmup: 100, GoroutinesPreWarmup: 20, GoroutinesCooldownGrowth: 2}, Samples: []TimeSeriesPoint{{At: time.Now(), Requests: 10, RPS: 10}}}}}
	if err := WriteArtifacts(directory, report); err != nil {
		t.Fatal(err)
	}
	loaded, err := ReadReport(filepath.Join(directory, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SchemaVersion != SchemaVersion {
		t.Fatalf("schema=%q", loaded.SchemaVersion)
	}
	if loaded.Scenario.UniqueQueryNamespace != "attempt-2" || loaded.Scenario.MaxAgentGoroutineGrowth != 256 || loaded.Runs[0].Resources.GoroutinesPreWarmup != 20 {
		t.Fatalf("schema 1.4 fields were not preserved: %+v", loaded)
	}
	for _, name := range []string{"report.json", "summary.md", "timeseries.csv"} {
		if _, err := os.Stat(filepath.Join(directory, name)); err != nil {
			t.Fatal(err)
		}
	}
	summary, err := os.ReadFile(filepath.Join(directory, "summary.md"))
	if err != nil || !strings.Contains(string(summary), "Captured response headers") || !strings.Contains(string(summary), "Cache activity") || !strings.Contains(string(summary), "HTTP status counts") || !strings.Contains(string(summary), "PRODUCT_FAIL") || !strings.Contains(string(summary), "http_5xx") {
		t.Fatalf("summary=%q err=%v", summary, err)
	}
}

func TestReadReportRejectsLegacySchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":"1.0"}`), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := ReadReport(path)
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("error=%v", err)
	}
}

func TestReadBaselineReportAcceptsComparableLegacySchemas(t *testing.T) {
	for _, schema := range []string{"1.2", "1.3", SchemaVersion} {
		path := filepath.Join(t.TempDir(), "report.json")
		if err := os.WriteFile(path, []byte(`{"schema_version":"`+schema+`"}`), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadBaselineReport(path); err != nil {
			t.Fatalf("schema %s baseline was rejected: %v", schema, err)
		}
	}
	path := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":"1.1"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadBaselineReport(path); err == nil {
		t.Fatal("incompatible schema 1.1 baseline was accepted")
	}
}

func TestWriteCSVKeepsCacheTelemetryColumnsAligned(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "timeseries.csv")
	report := Report{Runs: []Run{{Index: 1, Samples: []TimeSeriesPoint{{
		At: time.Unix(1, 0), CacheWriteQueueDepthMax: 17, CacheWriteQueueBytesMax: 19,
	}}}}}
	if err := writeCSV(path, report); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	records, err := csv.NewReader(file).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || len(records[0]) != len(records[1]) {
		t.Fatalf("CSV dimensions=%v", []int{len(records), len(records[0]), len(records[1])})
	}
	columns := make(map[string]int, len(records[0]))
	for index, name := range records[0] {
		columns[name] = index
	}
	for name, want := range map[string]string{
		"cache_write_queue_depth_max": "17",
		"cache_write_queue_bytes_max": "19",
	} {
		index, ok := columns[name]
		if !ok || records[1][index] != want {
			t.Fatalf("column %q=%q present=%v, want %q", name, records[1][index], ok, want)
		}
	}
}
