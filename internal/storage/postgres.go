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
	result, err := dbpush.Push(ctx, db, dbpush.Options{
		SchemaFS:         schemaFS,
		SchemaRoot:       ".",
		DatabaseURL:      databaseURL,
		AllowDestructive: true,
	})
	if err != nil {
		return nil, fmt.Errorf("apply database schema: %w", err)
	}
	if err := migrateLegacyNodeMemberships(ctx, db); err != nil {
		return nil, err
	}
	if err := normalizeOriginHealthPolicies(ctx, db); err != nil {
		return nil, err
	}
	return result, nil
}

func normalizeOriginHealthPolicies(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `UPDATE origin_pools
		SET governance = (governance - 'health_uri') || jsonb_build_object(
				'active_health', coalesce(governance->'active_health', '{}'::jsonb) || '{"enabled":false}'::jsonb,
				'passive_health', coalesce(governance->'passive_health', '{}'::jsonb) ||
					'{"unhealthy_status":[],"unhealthy_latency_ms":0,"unhealthy_request_count":0}'::jsonb
			),
			updated_at = now()
		WHERE governance ? 'health_uri'
			OR coalesce((governance->'active_health'->>'enabled')::boolean, false)
			OR coalesce(governance->'passive_health'->'unhealthy_status', '[]'::jsonb) <> '[]'::jsonb
			OR coalesce((governance->'passive_health'->>'unhealthy_latency_ms')::integer, 0) <> 0
			OR coalesce((governance->'passive_health'->>'unhealthy_request_count')::integer, 0) <> 0`)
	if err != nil {
		return fmt.Errorf("normalize origin health policies: %w", err)
	}
	return nil
}

func migrateLegacyNodeMemberships(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin legacy node membership migration: %w", err)
	}
	defer tx.Rollback()

	statements := []string{
		`INSERT INTO node_group_memberships (node_id, group_id)
		 SELECT id, group_id FROM nodes WHERE group_id IS NOT NULL
		 ON CONFLICT (node_id, group_id) DO NOTHING`,
		`INSERT INTO node_region_memberships (node_id, region_id)
		 SELECT id, region_id FROM nodes WHERE region_id IS NOT NULL
		 ON CONFLICT (node_id, region_id) DO NOTHING`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate legacy node memberships: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit legacy node membership migration: %w", err)
	}
	return nil
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
