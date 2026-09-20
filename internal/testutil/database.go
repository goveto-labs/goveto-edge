// Package testutil provides isolated databases for integration tests.
package testutil

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"goveto-edge/internal/storage"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/schema"
)

func Database(t *testing.T) *client.Client {
	t.Helper()
	raw := os.Getenv("GOVETO_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("GOVETO_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	admin, _, err := storage.OpenPostgreSQL(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	name := "goveto_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// WITH (FORCE) terminates connections still holding the database open;
		// it requires PostgreSQL 13 or newer.
		if _, err := admin.ExecContext(cleanup, "DROP DATABASE "+name+" WITH (FORCE)"); err != nil {
			t.Logf("drop test database %s: %v", name, err)
		}
		_ = admin.Close()
	})
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Path = "/" + name
	db, orm, err := storage.OpenPostgreSQL(ctx, parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err = storage.InitSchema(ctx, db, schema.FS, parsed.String()); err != nil {
		t.Fatal(err)
	}
	return orm
}
