// Package jobretention removes expired terminal job data and configuration snapshots.
package jobretention

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"goveto-edge/internal/settings"
	"goveto-edge/internal/storage/gen/client"
)

const batchSize = 500

type Worker struct {
	db       *client.Client
	settings *settings.Store
	interval time.Duration
}

type Stats struct {
	Jobs           int64
	Executions     int64
	ConfigVersions int64
}

type cleanupCounts struct {
	Jobs       int64 `db:"jobs"`
	Executions int64 `db:"executions"`
}

func New(db *client.Client, settingStore *settings.Store) *Worker {
	return &Worker{db: db, settings: settingStore, interval: time.Hour}
}

func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		started := time.Now()
		stats, err := w.Cleanup(ctx)
		if err != nil && ctx.Err() == nil {
			slog.Warn("clean up retained job data", "error", err)
		} else if err == nil && (stats.Jobs > 0 || stats.Executions > 0 || stats.ConfigVersions > 0) {
			slog.Info("cleaned up retained job data", "jobs", stats.Jobs, "executions", stats.Executions,
				"config_versions", stats.ConfigVersions, "duration", time.Since(started))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *Worker) Cleanup(ctx context.Context) (Stats, error) {
	policy, err := w.settings.JobRetention(ctx)
	if err != nil {
		return Stats{}, err
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -policy.HistoryDays)
	stats := Stats{}
	err = w.db.Tx(ctx, func(tx *client.Client) error {
		type lockRow struct {
			Locked bool `db:"locked"`
		}
		locks, lockErr := client.Raw[lockRow](ctx, tx, `SELECT pg_try_advisory_xact_lock(hashtext('job-retention')) AS locked`)
		if lockErr != nil {
			return lockErr
		}
		if len(locks) != 1 || !locks[0].Locked {
			return nil
		}

		for _, table := range []struct {
			name string
			kind string
		}{
			{"publish_jobs", "PUBLISH"}, {"purge_jobs", "PURGE"},
			{"install_jobs", "INSTALL"}, {"dns_sync_jobs", "DNS"},
			{"certificate_jobs", "CERTIFICATE"}, {"agent_upgrade_jobs", "AGENT_UPGRADE"},
		} {
			query := terminalJobCleanupSQL(table.name, table.kind)
			counts, execErr := client.Raw[cleanupCounts](ctx, tx, query, cutoff, batchSize)
			if execErr != nil {
				return execErr
			}
			if len(counts) != 1 {
				return fmt.Errorf("clean up %s: expected one count row, got %d", table.name, len(counts))
			}
			stats.Jobs += counts[0].Jobs
			stats.Executions += counts[0].Executions
		}

		result, execErr := tx.RawExec(ctx, orphanExecutionCleanupSQL, cutoff, batchSize)
		if execErr != nil {
			return execErr
		}
		stats.Executions += result

		result, execErr = tx.RawExec(ctx, configVersionCleanupSQL, cutoff, policy.VersionsPerSite, batchSize)
		if execErr != nil {
			return execErr
		}
		stats.ConfigVersions = result
		return nil
	})
	return stats, err
}

func terminalJobCleanupSQL(table, kind string) string {
	protected := ""
	if table == "publish_jobs" {
		protected = ` AND NOT (j.status='SUCCEEDED' AND j.id=(
			SELECT latest.id FROM publish_jobs latest WHERE latest.site_id=j.site_id
			AND latest.status='SUCCEEDED' ORDER BY latest.version DESC, latest.created_at DESC LIMIT 1))`
	}
	return fmt.Sprintf(`WITH doomed AS (
		SELECT j.id FROM %s j WHERE j.status IN ('SUCCEEDED','FAILED','DEAD_LETTER','CANCELLED')
		AND j.updated_at < $1%s ORDER BY j.updated_at LIMIT $2 FOR UPDATE SKIP LOCKED
	), deleted_executions AS (
		DELETE FROM job_executions e USING doomed d WHERE e.job_type='%s' AND e.job_id=d.id RETURNING e.id
	), deleted_jobs AS (
		DELETE FROM %s j USING doomed d WHERE j.id=d.id RETURNING j.id
	)
	SELECT (SELECT COUNT(*) FROM deleted_jobs) AS jobs,
		(SELECT COUNT(*) FROM deleted_executions) AS executions`, table, protected, kind, table)
}

const orphanExecutionCleanupSQL = `WITH doomed AS (
	SELECT e.id FROM job_executions e WHERE e.finished_at < $1 AND NOT EXISTS (
		SELECT 1 FROM publish_jobs j WHERE e.job_type='PUBLISH' AND j.id=e.job_id UNION ALL
		SELECT 1 FROM purge_jobs j WHERE e.job_type='PURGE' AND j.id=e.job_id UNION ALL
		SELECT 1 FROM install_jobs j WHERE e.job_type='INSTALL' AND j.id=e.job_id UNION ALL
		SELECT 1 FROM agent_upgrade_jobs j WHERE e.job_type='AGENT_UPGRADE' AND j.id=e.job_id UNION ALL
		SELECT 1 FROM dns_sync_jobs j WHERE e.job_type='DNS' AND j.id=e.job_id UNION ALL
		SELECT 1 FROM certificate_jobs j WHERE e.job_type='CERTIFICATE' AND j.id=e.job_id
	) ORDER BY e.finished_at LIMIT $2 FOR UPDATE SKIP LOCKED
)
DELETE FROM job_executions e USING doomed d WHERE e.id=d.id`

const configVersionCleanupSQL = `WITH ranked AS (
	SELECT cv.id, cv.site_id, cv.version, cv.created_at,
		ROW_NUMBER() OVER (PARTITION BY cv.site_id ORDER BY cv.version DESC) AS position,
		MAX(cv.version) FILTER (WHERE cv.status='PUBLISHED') OVER (PARTITION BY cv.site_id) AS latest_published
	FROM config_versions cv
), doomed AS (
	SELECT ranked.id FROM ranked JOIN sites s ON s.id=ranked.site_id
	WHERE ranked.created_at < $1 AND ranked.position > $2
	AND ranked.version <> s.version
	AND (ranked.latest_published IS NULL OR ranked.version <> ranked.latest_published)
	AND NOT EXISTS (SELECT 1 FROM publish_jobs j WHERE j.site_id=ranked.site_id AND j.version=ranked.version)
	ORDER BY ranked.created_at LIMIT $3
)
DELETE FROM config_versions cv USING doomed d WHERE cv.id=d.id`
