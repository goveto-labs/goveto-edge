package telemetry

import (
	"database/sql"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// Both pool kinds share the same metric names with a "pool" label so
// dashboards can aggregate across them.

func poolGauge(name, metric, help string, read func() float64) prometheus.GaugeFunc {
	return prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: "goveto", Subsystem: "db_pool", Name: metric,
		Help:        help,
		ConstLabels: prometheus.Labels{"pool": name},
	}, read)
}

// RegisterDBStats exports connection-pool statistics of a database/sql handle.
// The name label distinguishes pools (e.g. "control", "analytics").
func RegisterDBStats(name string, db *sql.DB) {
	MustRegister(
		poolGauge(name, "connections_open", "Open database connections.", func() float64 { return float64(db.Stats().OpenConnections) }),
		poolGauge(name, "connections_in_use", "Database connections currently in use.", func() float64 { return float64(db.Stats().InUse) }),
		poolGauge(name, "connections_idle", "Idle database connections.", func() float64 { return float64(db.Stats().Idle) }),
		poolGauge(name, "waits_total", "Cumulative waits for a database connection.", func() float64 { return float64(db.Stats().WaitCount) }),
	)
}

// RegisterPGXPoolStats exports connection-pool statistics of a pgx pool.
func RegisterPGXPoolStats(name string, pool *pgxpool.Pool) {
	MustRegister(
		poolGauge(name, "connections_open", "Open database connections.", func() float64 { return float64(pool.Stat().TotalConns()) }),
		poolGauge(name, "connections_in_use", "Database connections currently in use.", func() float64 { return float64(pool.Stat().AcquiredConns()) }),
		poolGauge(name, "connections_idle", "Idle database connections.", func() float64 { return float64(pool.Stat().IdleConns()) }),
	)
}
