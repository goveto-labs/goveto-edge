package node

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const installKeyPrefix = "node-install:"

type InstallQueue struct {
	redis *redis.Client
	ttl   time.Duration
}

func NewInstallQueue(client *redis.Client, ttl time.Duration) *InstallQueue {
	return &InstallQueue{redis: client, ttl: ttl}
}

func (q *InstallQueue) Enqueue(ctx context.Context, nodeID string, input SSHInstallInput) error {
	payload, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("encode SSH install input: %w", err)
	}
	if err := q.redis.Set(ctx, installKeyPrefix+nodeID, payload, q.ttl).Err(); err != nil {
		return fmt.Errorf("queue node installation: %w", err)
	}
	return nil
}

func (q *InstallQueue) Delete(ctx context.Context, nodeID string) error {
	return q.redis.Del(ctx, installKeyPrefix+nodeID).Err()
}
