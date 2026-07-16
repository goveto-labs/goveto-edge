package analytics

import (
	"context"
	"io/fs"
	"log/slog"
	"time"

	"goveto-edge/internal/storage"
)

type DailyRollup struct {
	store    *Store
	schemaFS fs.FS
}

func NewDailyRollup(store *Store, schemaFS fs.FS) *DailyRollup {
	return &DailyRollup{store: store, schemaFS: schemaFS}
}

func (r *DailyRollup) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	lastCompleted := ""
	for {
		today := time.Now().UTC().Format(time.DateOnly)
		if today != lastCompleted {
			if r.rollRecentDays(ctx) {
				lastCompleted = today
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *DailyRollup) rollRecentDays(ctx context.Context) bool {
	day := time.Now().UTC().AddDate(0, 0, -1)
	rollupCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	_, err := storage.ApplyClickHouseDailyRollup(rollupCtx, r.store.db, r.schemaFS, day)
	cancel()
	if err != nil {
		slog.Error("roll up ClickHouse analytics", "date", day.Format(time.DateOnly), "error", err)
		return false
	}
	return true
}
