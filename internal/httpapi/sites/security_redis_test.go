package sites

import (
	"testing"
	"time"

	securitypolicy "goveto-edge/internal/policy"
	"goveto-edge/internal/storage/gen/model"
)

func TestNodeBlocksRedisRateLimit(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-30 * time.Second)
	stale := now.Add(-3 * time.Minute)
	unavailable := false
	available := true

	for _, test := range []struct {
		name string
		node model.Node
		want bool
	}{
		{
			name: "online unavailable fresh",
			node: model.Node{
				Status: model.NodeStatusONLINE, RedisAvailable: &unavailable, HeartbeatAt: &fresh,
			},
			want: true,
		},
		{
			name: "online available",
			node: model.Node{
				Status: model.NodeStatusONLINE, RedisAvailable: &available, HeartbeatAt: &fresh,
			},
			want: false,
		},
		{
			name: "online unknown capability",
			node: model.Node{
				Status: model.NodeStatusONLINE, HeartbeatAt: &fresh,
			},
			want: false,
		},
		{
			name: "offline unavailable",
			node: model.Node{
				Status: model.NodeStatusOFFLINE, RedisAvailable: &unavailable, HeartbeatAt: &fresh,
			},
			want: false,
		},
		{
			name: "install failed unavailable",
			node: model.Node{
				Status: model.NodeStatusINSTALL_FAILED, RedisAvailable: &unavailable, HeartbeatAt: &fresh,
			},
			want: false,
		},
		{
			name: "online unavailable stale heartbeat",
			node: model.Node{
				Status: model.NodeStatusONLINE, RedisAvailable: &unavailable, HeartbeatAt: &stale,
			},
			want: false,
		},
		{
			name: "online unavailable missing heartbeat",
			node: model.Node{
				Status: model.NodeStatusONLINE, RedisAvailable: &unavailable,
			},
			want: false,
		},
	} {
		if got := nodeBlocksRedisRateLimit(test.node, now); got != test.want {
			t.Fatalf("%s: got %v want %v", test.name, got, test.want)
		}
	}
}

func TestEnsureRateLimitBackendCapacityShortCircuits(t *testing.T) {
	// nil db must not be touched when the gate does not apply.
	if err := ensureRateLimitBackendCapacity(t.Context(), nil, "cluster", securitypolicy.RateLimitPolicy{
		Enabled: false, Backend: "REDIS",
	}); err != nil {
		t.Fatal(err)
	}
	if err := ensureRateLimitBackendCapacity(t.Context(), nil, "cluster", securitypolicy.RateLimitPolicy{
		Enabled: true, Backend: "LOCAL",
	}); err != nil {
		t.Fatal(err)
	}
}
