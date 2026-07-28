package storage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	clickhouseschema "goveto-edge/configs/clickhouse"
)

type recordingClickHouseExecutor struct {
	statements []string
	failAt     int
}

func (e *recordingClickHouseExecutor) Exec(_ context.Context, statement string, _ ...any) error {
	e.statements = append(e.statements, statement)
	if e.failAt > 0 && len(e.statements) == e.failAt {
		return errors.New("exec failed")
	}
	return nil
}

func TestInitClickHouseSchemaAppliesStatementsInOrder(t *testing.T) {
	schemaFS := fstest.MapFS{"schema.sql": {Data: []byte(`
-- create the database first;
CREATE DATABASE IF NOT EXISTS goveto;
CREATE TABLE IF NOT EXISTS goveto.events (value String DEFAULT 'a;b');
/* additive migration; */
ALTER TABLE goveto.events ADD COLUMN IF NOT EXISTS count UInt64;
`)}}
	executor := &recordingClickHouseExecutor{}

	count, err := InitClickHouseSchema(context.Background(), executor, schemaFS)
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 || len(executor.statements) != 3 {
		t.Fatalf("expected 3 statements, got count=%d executed=%d", count, len(executor.statements))
	}
}

func TestInitClickHouseSchemaStopsOnFailure(t *testing.T) {
	schemaFS := fstest.MapFS{"schema.sql": {Data: []byte("SELECT 1; SELECT 2; SELECT 3;")}}
	executor := &recordingClickHouseExecutor{failAt: 2}

	count, err := InitClickHouseSchema(context.Background(), executor, schemaFS)
	if err == nil {
		t.Fatal("expected schema failure")
	}
	if count != 1 || len(executor.statements) != 2 {
		t.Fatalf("expected one successful statement and two attempts, got count=%d attempts=%d", count, len(executor.statements))
	}
}

func TestEmbeddedClickHouseSchemaIsExecutableStatementList(t *testing.T) {
	executor := &recordingClickHouseExecutor{}
	count, err := InitClickHouseSchema(context.Background(), executor, clickhouseschema.FS)
	if err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		t.Fatal("expected embedded schema statements")
	}
	joined := strings.Join(executor.statements, "\n")
	if !strings.Contains(joined, "CREATE TABLE IF NOT EXISTS goveto.node_runtime_metrics_minute") {
		t.Fatal("embedded schema does not create node runtime metrics table")
	}
	if !strings.Contains(joined, "CREATE TABLE IF NOT EXISTS goveto.origin_health_metrics_minute") {
		t.Fatal("embedded schema does not create origin health metrics table")
	}
	if !strings.Contains(joined, "ADD COLUMN IF NOT EXISTS waf_rule_id") {
		t.Fatal("embedded schema does not add WAF analytics columns")
	}
	if !strings.Contains(joined, "ADD COLUMN IF NOT EXISTS config_version") {
		t.Fatal("embedded schema does not add access-log config versions")
	}
}

func TestApplyClickHouseDailyRollupReplacesDate(t *testing.T) {
	schemaFS := fstest.MapFS{"rollup_daily.sql": {Data: []byte("SELECT toDate('{date}'); SELECT '{date}';")}}
	executor := &recordingClickHouseExecutor{}
	day := time.Date(2026, 7, 15, 23, 0, 0, 0, time.FixedZone("test", 8*60*60))
	count, err := ApplyClickHouseDailyRollup(context.Background(), executor, schemaFS, day)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 || strings.Contains(strings.Join(executor.statements, ""), "{date}") {
		t.Fatalf("daily rollup date was not applied: %#v", executor.statements)
	}
}

func TestSplitClickHouseStatementsRejectsUnterminatedInput(t *testing.T) {
	if _, err := splitClickHouseStatements("SELECT 'value;"); err == nil {
		t.Fatal("expected unterminated quote error")
	}
	if _, err := splitClickHouseStatements("SELECT 1; /* comment"); err == nil {
		t.Fatal("expected unterminated comment error")
	}
}
