package waf

import (
	"context"
	"strings"
	"testing"
	"time"
)

func resetRedisProbeCache() {
	redisProbeCache.Lock()
	defer redisProbeCache.Unlock()
	redisProbeCache.url = ""
	redisProbeCache.at = time.Time{}
	redisProbeCache.available = false
	redisProbeCache.err = ""
}

func TestCheckRedisBackendWithoutConfiguration(t *testing.T) {
	resetRedisProbeCache()
	t.Setenv("EDGE_AGENT_REDIS_URL", "")
	available, statusError := CheckRedisBackend(context.Background())
	if available {
		t.Fatal("backend reported available without EDGE_AGENT_REDIS_URL")
	}
	if !strings.Contains(statusError, "EDGE_AGENT_REDIS_URL") {
		t.Fatalf("status error = %q, want the missing configuration reason", statusError)
	}
}

func TestCheckRedisBackendUnreachable(t *testing.T) {
	resetRedisProbeCache()
	// Port 1 on localhost never answers a Redis handshake, so the probe must
	// report unavailable instead of blocking or panicking.
	t.Setenv("EDGE_AGENT_REDIS_URL", "redis://127.0.0.1:1/0")
	available, statusError := CheckRedisBackend(context.Background())
	if available {
		t.Fatal("backend reported available against an unreachable Redis")
	}
	if statusError == "" {
		t.Fatal("unreachable backend reported no status error")
	}
}

func TestCheckRedisBackendCachesProbeResult(t *testing.T) {
	resetRedisProbeCache()
	t.Setenv("EDGE_AGENT_REDIS_URL", "")
	firstAvailable, firstError := CheckRedisBackend(context.Background())
	secondAvailable, secondError := CheckRedisBackend(context.Background())
	if firstAvailable != secondAvailable || firstError != secondError {
		t.Fatalf("cache mismatch: (%v,%q) vs (%v,%q)", firstAvailable, firstError, secondAvailable, secondError)
	}
	redisProbeCache.Lock()
	cachedAt := redisProbeCache.at
	redisProbeCache.Unlock()
	if cachedAt.IsZero() {
		t.Fatal("probe result was not cached")
	}
}
