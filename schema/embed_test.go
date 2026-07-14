package schema

import (
	"context"
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
}
