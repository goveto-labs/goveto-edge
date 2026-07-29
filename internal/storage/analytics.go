package storage

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

const analyticsMigrationLock int64 = 0x47434f524d414e41

func OpenAnalyticsPostgreSQL(ctx context.Context, databaseURL string, maxConns int32) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse analytics database URL: %w", err)
	}
	config.MaxConns = maxConns
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("open analytics postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping analytics postgres: %w", err)
	}
	return pool, nil
}

// InitAnalyticsSchema serializes versioned migrations across control API replicas.
func InitAnalyticsSchema(ctx context.Context, pool *pgxpool.Pool, migrations fs.FS) (int, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return 0, fmt.Errorf("acquire analytics migration connection: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", analyticsMigrationLock); err != nil {
		return 0, fmt.Errorf("lock analytics migrations: %w", err)
	}
	defer conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", analyticsMigrationLock)

	if _, err := conn.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS timescaledb"); err != nil {
		return 0, fmt.Errorf("enable timescaledb extension: %w", err)
	}
	var version, preload string
	if err := conn.QueryRow(ctx, "SELECT extversion FROM pg_extension WHERE extname = 'timescaledb'").Scan(&version); err != nil {
		return 0, fmt.Errorf("verify timescaledb extension: %w", err)
	}
	if !strings.HasPrefix(version, "2.") {
		return 0, fmt.Errorf("timescaledb 2.x is required, found %s", version)
	}
	var postgresMajor int
	if err := conn.QueryRow(ctx, "SELECT current_setting('server_version_num')::integer / 10000").Scan(&postgresMajor); err != nil {
		return 0, fmt.Errorf("read postgres version: %w", err)
	}
	if postgresMajor != 18 {
		return 0, fmt.Errorf("postgres 18 is required for analytics, found major %d", postgresMajor)
	}
	if err := conn.QueryRow(ctx, "SHOW shared_preload_libraries").Scan(&preload); err != nil {
		return 0, fmt.Errorf("read shared_preload_libraries: %w", err)
	}
	loaded := false
	for _, library := range strings.Split(preload, ",") {
		loaded = loaded || strings.TrimSpace(library) == "timescaledb"
	}
	if !loaded {
		return 0, fmt.Errorf("shared_preload_libraries must include timescaledb")
	}

	if _, err := conn.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS analytics;
		CREATE TABLE IF NOT EXISTS analytics.schema_migrations (
			version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now()
		)`); err != nil {
		return 0, fmt.Errorf("create analytics migration ledger: %w", err)
	}
	entries, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return 0, fmt.Errorf("list analytics migrations: %w", err)
	}
	sort.Strings(entries)
	applied := 0
	for _, name := range entries {
		version := path.Base(name)
		var exists bool
		if err := conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM analytics.schema_migrations WHERE version = $1)", version).Scan(&exists); err != nil {
			return applied, fmt.Errorf("check analytics migration %s: %w", version, err)
		}
		if exists {
			continue
		}
		sql, err := fs.ReadFile(migrations, name)
		if err != nil {
			return applied, fmt.Errorf("read analytics migration %s: %w", version, err)
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return applied, fmt.Errorf("begin analytics migration %s: %w", version, err)
		}
		if _, err = tx.Exec(ctx, string(sql)); err == nil {
			_, err = tx.Exec(ctx, "INSERT INTO analytics.schema_migrations (version) VALUES ($1)", version)
		}
		if err == nil {
			err = tx.Commit(ctx)
		} else {
			_ = tx.Rollback(ctx)
		}
		if err != nil {
			return applied, fmt.Errorf("apply analytics migration %s: %w", version, err)
		}
		applied++
	}
	return applied, nil
}
