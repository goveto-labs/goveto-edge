package analytics

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

func TestLiveLogsAcrossReplicasAndReconnectIntegration(t *testing.T) {
	address := os.Getenv("GOVETO_TEST_REDIS_URL")
	if address == "" {
		t.Skip("GOVETO_TEST_REDIS_URL is not set")
	}
	options, err := redis.ParseURL(address)
	if err != nil {
		t.Fatal(err)
	}
	a, b := redis.NewClient(options), redis.NewClient(options)
	t.Cleanup(func() { a.Close(); b.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cluster := uuid.NewString()
	t.Cleanup(func() { a.Del(context.Background(), liveStreamKey(cluster)) })
	producer, subscriber := NewStore(nil, time.Second, a), NewStore(nil, time.Second, b)
	t.Cleanup(func() { producer.Close(); subscriber.Close() })
	stream, err := subscriber.SubscribeFrom(ctx, LiveFilter{ClusterID: cluster, SiteID: "site"}, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	producer.publish([]WebRequestLog{{ClusterID: cluster, SiteID: "other", Path: "/hidden"}, {ClusterID: cluster, SiteID: "site", Path: "/first"}})
	next := func(events <-chan LiveRequestLog) LiveRequestLog {
		t.Helper()
		select {
		case event := <-events:
			return event
		case <-ctx.Done():
			t.Fatal("live event timed out")
			return LiveRequestLog{}
		}
	}
	first := next(stream)
	if first.Path != "/first" || first.Cursor == "" {
		t.Fatalf("first=%+v", first)
	}
	producer.publish([]WebRequestLog{{ClusterID: cluster, SiteID: "site", Path: "/second"}})
	reconnected, err := producer.SubscribeFrom(ctx, LiveFilter{ClusterID: cluster, SiteID: "site"}, 1, first.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if second := next(reconnected); second.Path != "/second" || second.Cursor == first.Cursor {
		t.Fatalf("replay=%+v", second)
	}
	if err := a.XTrimMaxLen(ctx, liveStreamKey(cluster), 1).Err(); err != nil {
		t.Fatal(err)
	}
	trimmed, err := subscriber.SubscribeFrom(ctx, LiveFilter{ClusterID: cluster, SiteID: "site"}, 1, first.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if event := next(trimmed); !event.Gap {
		t.Fatalf("missing gap notice: %+v", event)
	}
	if event := next(trimmed); event.Path != "/second" {
		t.Fatalf("retained replay: %+v", event)
	}
}

func TestSubscribeFromRejectsInvalidCursor(t *testing.T) {
	store := NewStore(nil, time.Second)
	events, err := store.SubscribeFrom(context.Background(), LiveFilter{ClusterID: "cluster-1"}, 1, "not-a-cursor")
	if !errors.Is(err, ErrInvalidLiveCursor) {
		t.Fatalf("err=%v, want ErrInvalidLiveCursor", err)
	}
	if events != nil {
		t.Fatal("expected nil channel for invalid cursor")
	}
}

func TestSubscribeFromInProcessFallback(t *testing.T) {
	store := NewStore(nil, time.Second) // no Redis client: liveRedis == nil
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := store.SubscribeFrom(ctx, LiveFilter{ClusterID: "cluster-1"}, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	store.publish([]WebRequestLog{
		{ClusterID: "cluster-2", Path: "/hidden"},
		{ClusterID: "cluster-1", Path: "/fallback"},
	})
	select {
	case event := <-events:
		if event.Path != "/fallback" {
			t.Fatalf("event=%+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("in-process live event timed out")
	}
}

func TestSubscribeFallsBackWhenRedisFails(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6399"}) // nothing listening
	t.Cleanup(func() { client.Close() })
	store := NewStore(nil, 100*time.Millisecond, client)
	t.Cleanup(store.Close)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := store.Subscribe(ctx, LiveFilter{ClusterID: "cluster-1"}, 1)
	if events == nil {
		t.Fatal("expected fallback in-process channel")
	}
	store.live.Publish([]WebRequestLog{{ClusterID: "cluster-1", Path: "/local"}})
	select {
	case event := <-events:
		if event.Path != "/local" {
			t.Fatalf("event=%+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("fallback live event timed out")
	}
}

func TestPublishAsyncLifecycle(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6399"}) // nothing listening
	t.Cleanup(func() { client.Close() })
	store := NewStore(nil, time.Second, client)
	store.publish([]WebRequestLog{{ClusterID: "cluster-1"}}) // queued; worker fails against unreachable Redis
	store.Close()
	store.Close() // idempotent
	store.publish([]WebRequestLog{{ClusterID: "cluster-1"}})
	if got := store.publishDropped.Load(); got == 0 {
		t.Fatal("expected dropped events after Close")
	}
}

func TestCursorBeforeDefendsMalformedIDs(t *testing.T) {
	if cursorBefore("garbage", "1-0") || cursorBefore("1-0", "garbage") || cursorBefore("", "") {
		t.Fatal("malformed IDs must not report a gap")
	}
	if !cursorBefore("1-0", "1-1") || !cursorBefore("1-5", "2-0") {
		t.Fatal("valid ordering rejected")
	}
	if cursorBefore("2-0", "1-5") || cursorBefore("1-1", "1-1") {
		t.Fatal("invalid ordering accepted")
	}
}
