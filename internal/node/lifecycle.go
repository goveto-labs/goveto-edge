package node

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"reflect"
	"time"

	"goveto-edge/internal/edgecontrol"
	"goveto-edge/internal/edgeprotocol"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/query"
)

type Lifecycle struct {
	db             *client.Client
	offlineAfter   time.Duration
	http           *http.Client
	cipher         *CredentialCipher
	onStatusChange func(context.Context, string)
	healthFailures map[string]string
}

type healthTarget struct {
	NodeID    string `db:"node_id"`
	ClusterID string `db:"cluster_id"`
	Address   string `db:"address"`
	Status    string `db:"status"`
}

func NewLifecycle(
	db *client.Client,
	cipher *CredentialCipher,
	offlineAfter time.Duration,
	onStatusChange ...func(context.Context, string),
) *Lifecycle {
	lifecycle := &Lifecycle{
		db:             db,
		cipher:         cipher,
		offlineAfter:   offlineAfter,
		http:           &http.Client{Timeout: 5 * time.Second},
		healthFailures: map[string]string{},
	}
	if len(onStatusChange) > 0 {
		lifecycle.onStatusChange = onStatusChange[0]
	}
	return lifecycle
}
func (l *Lifecycle) Run(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		l.poll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (l *Lifecycle) poll(ctx context.Context) {
	targets, err := client.Raw[healthTarget](ctx, l.db, `SELECT n.id AS node_id, n.cluster_id, n.status, a.address
		FROM nodes n
		JOIN node_addresses a ON a.node_id = n.id AND a.primary = TRUE
		WHERE n.status NOT IN ('PENDING', 'INSTALLING', 'INSTALL_FAILED', 'DISABLED')`)
	if err != nil {
		slog.Error("query node health targets", "error", err)
		return
	}

	for _, target := range targets {
		request, err := newHealthRequest(ctx, target)
		if err != nil {
			l.logHealthFailure(target, "build request: "+err.Error())
			continue
		}

		response, err := l.http.Do(request)
		if err != nil {
			l.logHealthFailure(target, "request failed: "+err.Error())
			continue
		}

		var health struct {
			NodeID      string                       `json:"node_id"`
			CacheConfig edgeprotocol.NodeCacheConfig `json:"cache_config"`
		}
		decodeErr := json.NewDecoder(response.Body).Decode(&health)
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			l.logHealthFailure(target, fmt.Sprintf("unexpected HTTP status %s", response.Status))
			continue
		}
		if decodeErr != nil {
			l.logHealthFailure(target, "decode response: "+decodeErr.Error())
			continue
		}
		if health.NodeID != target.NodeID {
			l.logHealthFailure(
				target,
				fmt.Sprintf("node ID mismatch: got %q", health.NodeID),
			)
			continue
		}
		if _, failed := l.healthFailures[target.NodeID]; failed {
			slog.Info(
				"node health check recovered",
				"node_id", target.NodeID,
				"address", target.Address,
			)
			delete(l.healthFailures, target.NodeID)
		}

		_, _ = l.db.RawExec(
			ctx,
			`UPDATE nodes SET status = 'ONLINE', install_error = NULL, heartbeat_at = NOW(), updated_at = NOW() WHERE id = $1`,
			target.NodeID,
		)
		if target.Status != "ONLINE" {
			slog.Info(
				"node is online",
				"node_id", target.NodeID,
				"address", target.Address,
			)
			l.notifyStatusChange(ctx, target.ClusterID)
		}
		l.reconcileCacheConfig(ctx, target, health.CacheConfig)
	}
	l.markOffline(ctx)
}

func newHealthRequest(ctx context.Context, target healthTarget) (*http.Request, error) {
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		"http://"+net.JoinHostPort(target.Address, "80")+"/v1/health",
		nil,
	)
	if err != nil {
		return nil, err
	}
	// The Agent API is routed by Caddy using the node UUID as its Host matcher.
	request.Host = target.NodeID
	return request, nil
}

func (l *Lifecycle) logHealthFailure(target healthTarget, reason string) {
	if l.healthFailures[target.NodeID] == reason {
		return
	}
	l.healthFailures[target.NodeID] = reason
	slog.Warn(
		"node health check failed",
		"node_id", target.NodeID,
		"address", target.Address,
		"host", target.NodeID,
		"reason", reason,
	)
}

func (l *Lifecycle) reconcileCacheConfig(ctx context.Context, target healthTarget, current edgeprotocol.NodeCacheConfig) {
	stored, err := l.db.NodeCacheConfig.FindUnique(ctx, query.NodeCacheConfig.NodeId.Equals(target.NodeID))
	if err != nil {
		return
	}

	desired := edgeprotocol.NodeCacheConfig{
		CacheDirectory:      stored.CacheDir,
		AutoMaxSize:         stored.AutoMaxSize,
		MaxDiskUsagePercent: stored.MaxDiskUsagePercent,
	}
	if stored.MaxSizeBytes != nil {
		desired.MaxSizeBytes = uint64(*stored.MaxSizeBytes)
	}
	if reflect.DeepEqual(current, desired) {
		return
	}

	credential, err := l.db.NodeCredential.FindUnique(ctx, query.NodeCredential.NodeId.Equals(target.NodeID))
	if err != nil {
		return
	}

	key, err := l.cipher.Decrypt(credential.CommunicationKeyEncrypted)
	if err != nil {
		return
	}

	_ = edgecontrol.New(
		"http://"+net.JoinHostPort(target.Address, "80"),
		target.NodeID,
		key,
	).PushNodeCacheConfig(ctx, desired)
}
func (l *Lifecycle) markOffline(ctx context.Context) {
	type changedCluster struct {
		ClusterID string `db:"cluster_id"`
	}
	clusters, err := client.Raw[changedCluster](ctx, l.db, `UPDATE nodes SET status = 'OFFLINE', updated_at = NOW()
			WHERE status = 'ONLINE' AND (heartbeat_at IS NULL OR heartbeat_at < NOW() - ($1 * INTERVAL '1 second'))
			RETURNING cluster_id`, l.offlineAfter.Seconds())
	if err != nil {
		return
	}
	notified := map[string]struct{}{}
	for _, cluster := range clusters {
		if _, exists := notified[cluster.ClusterID]; exists {
			continue
		}
		notified[cluster.ClusterID] = struct{}{}
		l.notifyStatusChange(ctx, cluster.ClusterID)
	}
}

func (l *Lifecycle) notifyStatusChange(ctx context.Context, clusterID string) {
	if l.onStatusChange != nil {
		l.onStatusChange(ctx, clusterID)
	}
}
