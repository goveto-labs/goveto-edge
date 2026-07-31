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
	return result, nil
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
