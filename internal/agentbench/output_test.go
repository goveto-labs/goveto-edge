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
	report := Report{SchemaVersion: SchemaVersion, Platform: Platform{Architecture: "amd64"}, Scenario: Scenario{Name: "origin", Protocol: ProtocolH1}, Summary: Metrics{ResponseHeaders: map[string]map[string]uint64{"X-Cache": {"HIT": 10}}}, Validity: Validity{Valid: true}, Runs: []Run{{Index: 1, Resources: ResourceSummary{CacheHitsDelta: 10}, Samples: []TimeSeriesPoint{{At: time.Now(), Requests: 10, RPS: 10}}}}}
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
	if err != nil || !strings.Contains(string(summary), "Captured response headers") || !strings.Contains(string(summary), "Cache activity") {
		t.Fatalf("summary=%q err=%v", summary, err)
	}
}

func TestReadReportRejectsUnknownSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":"2.0"}`), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := ReadReport(path)
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("error=%v", err)
	}
}
