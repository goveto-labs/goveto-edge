package node

import (
	"context"
	"encoding/json"
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
	db           *client.Client
	offlineAfter time.Duration
	http         *http.Client
	cipher       *CredentialCipher
}

type healthTarget struct {
	NodeID  string `json:"node_id"`
	Address string `json:"address"`
}

func NewLifecycle(db *client.Client, cipher *CredentialCipher, offlineAfter time.Duration) *Lifecycle {
	return &Lifecycle{
		db:           db,
		cipher:       cipher,
		offlineAfter: offlineAfter,
		http:         &http.Client{Timeout: 5 * time.Second},
	}
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
	targets, err := client.Raw[healthTarget](ctx, l.db, `SELECT n.id AS node_id, a.address
		FROM nodes n
		JOIN node_addresses a ON a.node_id = n.id AND a.primary = TRUE
		WHERE n.status NOT IN ('PENDING', 'INSTALL_FAILED')`)
	if err != nil {
		return
	}

	for _, target := range targets {
		request, err := http.NewRequestWithContext(
			ctx,
			http.MethodGet,
			"http://"+net.JoinHostPort(target.Address, "80")+"/v1/health",
			nil,
		)
		if err != nil {
			continue
		}

		response, err := l.http.Do(request)
		if err != nil {
			continue
		}

		var health struct {
			NodeID      string                       `json:"node_id"`
			CacheConfig edgeprotocol.NodeCacheConfig `json:"cache_config"`
		}
		decodeErr := json.NewDecoder(response.Body).Decode(&health)
		response.Body.Close()
		if response.StatusCode != http.StatusOK || decodeErr != nil || health.NodeID != target.NodeID {
			continue
		}

		_, _ = l.db.RawExec(
			ctx,
			`UPDATE nodes SET status = 'ONLINE', heartbeat_at = NOW(), updated_at = NOW() WHERE id = $1`,
			target.NodeID,
		)
		l.reconcileCacheConfig(ctx, target, health.CacheConfig)
	}
	l.markOffline(ctx)
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
	_, _ = l.db.RawExec(ctx, `UPDATE nodes SET status = 'OFFLINE', updated_at = NOW()
		WHERE status = 'ONLINE' AND (heartbeat_at IS NULL OR heartbeat_at < NOW() - ($1 * INTERVAL '1 second'))`, l.offlineAfter.Seconds())
}
