package storage

import (
	"context"
	"fmt"

	"github.com/ClickHouse/clickhouse-go/v2"
)

func OpenClickHouse(ctx context.Context, dsn string) (clickhouse.Conn, error) {
	options, err := clickhouse.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse ClickHouse DSN: %w", err)
	}
	connection, err := clickhouse.Open(options)
	if err != nil {
		return nil, fmt.Errorf("open ClickHouse: %w", err)
	}
	if err := connection.Ping(ctx); err != nil {
		connection.Close()
		return nil, fmt.Errorf("ping ClickHouse: %w", err)
	}
	return connection, nil
}
