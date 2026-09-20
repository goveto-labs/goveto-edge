package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const liveStreamLength = 10000

// ErrInvalidLiveCursor marks a malformed Last-Event-ID so HTTP handlers can
// restart the stream from the latest event instead of rejecting the request.
var ErrInvalidLiveCursor = errors.New("invalid live log cursor")

var liveCursorPattern = regexp.MustCompile(`^[0-9]{1,20}-[0-9]{1,20}$`)

func liveStreamKey(clusterID string) string { return "goveto:live:" + clusterID }

func (s *Store) publishShared(events []WebRequestLog) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	pipe := s.liveRedis.Pipeline()
	expires := map[string]struct{}{}
	for _, event := range events {
		if event.ClusterID == "" {
			continue
		}
		encoded, err := json.Marshal(liveRequestLog(event))
		if err != nil {
			continue
		}
		key := liveStreamKey(event.ClusterID)
		pipe.XAdd(ctx, &redis.XAddArgs{Stream: key, MaxLen: liveStreamLength, Approx: true, Values: map[string]any{"event": string(encoded)}})
		if _, ok := expires[key]; !ok {
			pipe.Expire(ctx, key, 24*time.Hour)
			expires[key] = struct{}{}
		}
	}
	if _, err := pipe.Exec(ctx); err != nil {
		slog.Warn("live log delivery unavailable; durable logs remain queryable", "error", err)
	}
}

// SubscribeFrom replays retained events after a cluster-scoped SSE cursor.
// Slow consumers back up in Redis, not in the ingest process.
func (s *Store) SubscribeFrom(ctx context.Context, filter LiveFilter, buffer int, cursor string) (<-chan LiveRequestLog, error) {
	if cursor != "" && !liveCursorPattern.MatchString(cursor) {
		return nil, ErrInvalidLiveCursor
	}
	if s.liveRedis == nil {
		return s.live.Subscribe(ctx, filter, buffer), nil
	}
	if filter.ClusterID == "" {
		return nil, errors.New("live log cluster is required")
	}
	key := liveStreamKey(filter.ClusterID)
	if cursor == "" {
		queryCtx, cancel := context.WithTimeout(ctx, s.queryTimeout)
		latest, err := s.liveRedis.XRevRangeN(queryCtx, key, "+", "-", 1).Result()
		cancel()
		if err != nil {
			return nil, err
		}
		cursor = "0-0"
		if len(latest) > 0 {
			cursor = latest[0].ID
		}
	}
	if buffer < 1 {
		buffer = 256
	}
	events := make(chan LiveRequestLog, buffer)
	go func() {
		defer close(events)
		emit := func(event LiveRequestLog) bool {
			select {
			case events <- event:
				return true
			case <-ctx.Done():
				return false
			}
		}
		checkGap := func() bool {
			oldest, err := s.liveRedis.XRangeN(ctx, key, "-", "+", 1).Result()
			if err == nil && len(oldest) > 0 && cursor != "0-0" && cursorBefore(cursor, oldest[0].ID) {
				if !emit(LiveRequestLog{Gap: true}) {
					return false
				}
				cursor = "0-0"
			}
			return true
		}
		if !checkGap() {
			return
		}
		for ctx.Err() == nil {
			streams, err := s.liveRedis.XRead(ctx, &redis.XReadArgs{Streams: []string{key, cursor}, Count: 256, Block: time.Second}).Result()
			if errors.Is(err, redis.Nil) {
				continue
			}
			if err != nil {
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Second):
				}
				continue
			}
			if !checkGap() {
				return
			}
			for _, stream := range streams {
				for _, message := range stream.Messages {
					var event LiveRequestLog
					raw, _ := message.Values["event"].(string)
					cursor = message.ID
					if json.Unmarshal([]byte(raw), &event) != nil {
						continue
					}
					if !filter.matches(event) {
						continue
					}
					event.Cursor = message.ID
					if !emit(event) {
						return
					}
				}
			}
		}
	}()
	return events, nil
}

func cursorBefore(left, right string) bool {
	l, r := strings.SplitN(left, "-", 2), strings.SplitN(right, "-", 2)
	if len(l) != 2 || len(r) != 2 {
		return false
	}
	lTime, _ := strconv.ParseUint(l[0], 10, 64)
	rTime, _ := strconv.ParseUint(r[0], 10, 64)
	lSequence, _ := strconv.ParseUint(l[1], 10, 64)
	rSequence, _ := strconv.ParseUint(r[1], 10, 64)
	return lTime < rTime || (lTime == rTime && lSequence < rSequence)
}
