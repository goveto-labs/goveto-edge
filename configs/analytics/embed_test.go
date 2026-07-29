package analyticsschema

import (
	"io/fs"
	"strings"
	"testing"
)

func TestInitialMigrationContainsTimescaleContracts(t *testing.T) {
	raw, err := fs.ReadFile(FS, "migrations/001_timescaledb.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	for _, required := range []string{
		"CREATE SCHEMA IF NOT EXISTS analytics",
		"create_hypertable('analytics.web_request_logs'",
		"UNIQUE (event_time, node_id, source_log_id)",
		"add_compression_policy('analytics.web_request_logs', INTERVAL '6 hours'",
		"analytics.request_usage_hourly",
		"analytics.request_usage_daily",
		"analytics.node_traffic_metrics_minute",
		"analytics.daily_unique_ips",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("migration is missing %q", required)
		}
	}
	if strings.Contains(sql, "add_continuous_aggregate_policy") {
		t.Fatal("refresh policies must be configured from the runtime raw-retention setting")
	}
}
