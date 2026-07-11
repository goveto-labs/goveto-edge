// Package storage contains PostgreSQL, Redis, and ClickHouse adapters.
package storage

import (
	"context"
	"database/sql"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib"

	"goveto-edge/internal/storage/gen/client"
)

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
