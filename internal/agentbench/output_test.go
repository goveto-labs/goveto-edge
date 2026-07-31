package agentbench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteArtifactsAndReadReport(t *testing.T) {
	directory := t.TempDir()
	report := Report{SchemaVersion: SchemaVersion, RunnerID: "agent4", Platform: Platform{Architecture: "amd64"}, Scenario: Scenario{Name: "origin", Protocol: ProtocolH1}, Summary: Metrics{ResponseHeaders: map[string]map[string]uint64{"X-Cache": {"HIT": 10}}, HTTPStatusCounts: map[int]uint64{200: 8, 502: 2}}, Validity: Validity{Valid: false, Status: ResultProductFail, Reasons: []string{"request failed"}}, ErrorCounts: map[string]uint64{"http_5xx": 2}, Runs: []Run{{Index: 1, Resources: ResourceSummary{CacheHitsDelta: 10}, Samples: []TimeSeriesPoint{{At: time.Now(), Requests: 10, RPS: 10}}}}}
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
