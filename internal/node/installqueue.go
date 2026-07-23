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

func (q *InstallQueue) Enqueue(ctx context.Context, nodeID string, input InstallPayload) error {
	payload, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("encode node install payload: %w", err)
	}
	if err := q.redis.Set(ctx, installKeyPrefix+nodeID, payload, q.ttl).Err(); err != nil {
		return fmt.Errorf("queue node installation: %w", err)
	}
	return nil
}

func (q *InstallQueue) Delete(ctx context.Context, nodeID string) error {
	return q.redis.Del(ctx, installKeyPrefix+nodeID).Err()
}

func (q *InstallQueue) Claim(ctx context.Context) (*InstallPayload, error) {
	var cursor uint64
	for {
		keys, next, err := q.redis.Scan(ctx, cursor, installKeyPrefix+"*", 20).Result()
		if err != nil {
			return nil, err
		}
		for _, key := range keys {
			value, err := q.redis.GetDel(ctx, key).Bytes()
			if err == redis.Nil {
				continue
			}
			if err != nil {
				return nil, err
			}
			var payload InstallPayload
			if err := json.Unmarshal(value, &payload); err != nil {
				return nil, err
			}
			return &payload, nil
		}
		if next == 0 {
			return nil, nil
		}
		cursor = next
	}
}
