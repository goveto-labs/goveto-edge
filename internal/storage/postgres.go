// Package storage contains PostgreSQL, TimescaleDB, and Redis adapters.
package storage

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"

	"github.com/arsfy/gcorm/pkg/tooling/dbpush"
	_ "github.com/jackc/pgx/v5/stdlib"

	"goveto-edge/internal/storage/gen/client"
)

func InitSchema(ctx context.Context, db *sql.DB, schemaFS fs.FS, databaseURL string) (*dbpush.Result, error) {
	if err := prepareAlertActiveFingerprintIndex(ctx, db); err != nil {
		return nil, fmt.Errorf("prepare alert active fingerprint index: %w", err)
	}
	result, err := dbpush.Push(ctx, db, dbpush.Options{
		SchemaFS:         schemaFS,
		SchemaRoot:       ".",
		DatabaseURL:      databaseURL,
		AllowDestructive: false,
	})
	if err != nil {
		return nil, fmt.Errorf("apply database schema: %w", err)
	}
	return result, nil
}

// prepareAlertActiveFingerprintIndex handles upgrades from alert schemas that
// predate the active-instance uniqueness invariant. Fresh databases let GCORM
// create the index with the rest of the schema. Existing databases first
// resolve duplicate active rows, then build the index without blocking writes.
func prepareAlertActiveFingerprintIndex(ctx context.Context, db *sql.DB) error {
	var exists bool
	if err := db.QueryRowContext(ctx,
		`SELECT to_regclass('public.alert_instances') IS NOT NULL`,
	).Scan(&exists); err != nil || !exists {
		return err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx,
		`SELECT pg_advisory_lock(hashtextextended('schema:alert_instances_active_fingerprint', 0))`,
	); err != nil {
		return err
	}
	defer func() {
		_, _ = conn.ExecContext(context.WithoutCancel(ctx),
			`SELECT pg_advisory_unlock(hashtextextended('schema:alert_instances_active_fingerprint', 0))`)
	}()
	var indexValid bool
	if err = conn.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM pg_class c JOIN pg_index i ON i.indexrelid=c.oid
		WHERE c.relname='alert_instances_active_fingerprint_key' AND i.indisvalid
	)`).Scan(&indexValid); err != nil {
		return err
	}
	if indexValid {
		return nil
	}
	if _, err = conn.ExecContext(ctx,
		`DROP INDEX CONCURRENTLY IF EXISTS alert_instances_active_fingerprint_key`,
	); err != nil {
		return err
	}

	if _, err = conn.ExecContext(ctx, `WITH ranked AS (
		SELECT id, ROW_NUMBER() OVER (
			PARTITION BY rule_id, fingerprint ORDER BY updated_at DESC, id DESC
		) AS position
		FROM alert_instances WHERE status <> 'RESOLVED'
	)
	UPDATE alert_instances i SET status='RESOLVED', resolved_at=NOW(),
		resolved_reason='deduplicated during active fingerprint index migration', updated_at=NOW()
	FROM ranked r WHERE i.id=r.id AND r.position > 1`); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS
		alert_instances_active_fingerprint_key ON alert_instances
		(rule_id, (CASE WHEN status <> 'RESOLVED' THEN fingerprint END))`)
	return err
}

func OpenPostgreSQL(ctx context.Context, databaseURL string) (*sql.DB, *client.Client, error) {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, nil, fmt.Errorf("open postgres: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("ping postgres: %w", err)
	}
	return db, client.New(db, client.WithDialect("postgresql")), nil
}
