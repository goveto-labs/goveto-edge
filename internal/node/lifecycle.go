package node

import (
	"context"
	"log/slog"
	"time"

	"goveto-edge/internal/storage/gen/client"
)

// Lifecycle marks nodes offline when their active management stream stops
// producing heartbeats. Online transitions are recorded by the mTLS gateway.
type Lifecycle struct {
	db             *client.Client
	offlineAfter   time.Duration
	onStatusChange func(context.Context, string)
}

func NewLifecycle(
	db *client.Client,
	offlineAfter time.Duration,
	onStatusChange ...func(context.Context, string),
) *Lifecycle {
	lifecycle := &Lifecycle{db: db, offlineAfter: offlineAfter}
	if len(onStatusChange) > 0 {
		lifecycle.onStatusChange = onStatusChange[0]
	}
	return lifecycle
}

func (l *Lifecycle) Run(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		l.markOffline(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (l *Lifecycle) markOffline(ctx context.Context) {
	type changedCluster struct {
		ClusterID string `db:"cluster_id"`
	}
	clusters, err := client.Raw[changedCluster](ctx, l.db, `UPDATE nodes SET status = 'OFFLINE', updated_at = NOW()
		WHERE status = 'ONLINE' AND (heartbeat_at IS NULL OR heartbeat_at < NOW() - ($1 * INTERVAL '1 second'))
		RETURNING cluster_id`, l.offlineAfter.Seconds())
	if err != nil {
		slog.Warn("mark disconnected agents offline", "error", err)
		return
	}
	notified := map[string]struct{}{}
	for _, cluster := range clusters {
		if _, exists := notified[cluster.ClusterID]; exists {
			continue
		}
		notified[cluster.ClusterID] = struct{}{}
		if l.onStatusChange != nil {
			l.onStatusChange(ctx, cluster.ClusterID)
		}
	}
}
