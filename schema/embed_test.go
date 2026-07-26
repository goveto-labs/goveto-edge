package schema

import (
	"context"
	"strings"
	"testing"

	"github.com/arsfy/gcorm/pkg/tooling/dbpush"
)

func TestEmbeddedSchema(t *testing.T) {
	data, err := FS.ReadFile("schema.gcorm")
	if err != nil {
		t.Fatalf("read embedded schema: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("embedded schema is empty")
	}
}

func TestEmbeddedSchemaCompilesForDBPush(t *testing.T) {
	result, err := dbpush.Push(context.Background(), nil, dbpush.Options{
		SchemaFS:          FS,
		SchemaRoot:        ".",
		DryRun:            true,
		SkipIntrospection: true,
	})
	if err != nil {
		t.Fatalf("compile embedded schema: %v", err)
	}
	if result.ModelCount == 0 || len(result.Statements) == 0 {
		t.Fatalf("unexpected empty schema plan: models=%d statements=%d", result.ModelCount, len(result.Statements))
	}
	plan := strings.Join(result.Statements, "\n")
	for _, expected := range []string{
		`"next_attempt_at" TIMESTAMPTZ NOT NULL DEFAULT NOW()`,
		`CREATE INDEX "idx_agent_tasks_status_next_attempt_at" ON "agent_tasks" ("status", "next_attempt_at")`,
		`'DEAD_LETTER'`,
	} {
		if !strings.Contains(plan, expected) {
			t.Fatalf("schema plan is missing %q", expected)
		}
	}
}
