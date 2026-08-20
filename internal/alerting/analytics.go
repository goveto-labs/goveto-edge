package alerting

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// AnalyticsPool adapts a *pgxpool.Pool to AnalyticsQuerier.
func AnalyticsPool(pool *pgxpool.Pool) AnalyticsQuerier {
	if pool == nil {
		return nil
	}
	return poolAdapter{pool: pool}
}

type poolAdapter struct{ pool *pgxpool.Pool }

func (a poolAdapter) QueryAnalytics(ctx context.Context, sql string, scan func(row AnalyticsRow) error, args ...any) error {
	rows, err := a.pool.Query(ctx, sql, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}
