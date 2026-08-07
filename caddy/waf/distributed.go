package waf

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"goveto-edge/internal/policy"
	"goveto-edge/internal/securitystate"
)

type distributedStore interface {
	Allow(context.Context, string, string, string, policy.RateLimitRule) (bool, time.Duration, error)
	Blocked(context.Context, string, string) (bool, time.Duration, error)
}

type challengeStateStore interface {
	PutChallenge(context.Context, string, time.Duration) error
	ConsumeChallenge(context.Context, string) (bool, error)
}

type redisStore struct{ client *redis.Client }

var redisRateScript = redis.NewScript(`
local ban_ttl = redis.call('PTTL', KEYS[1])
if ban_ttl > 0 then
  return {0, ban_ttl}
end
local count = redis.call('INCR', KEYS[2])
if count == 1 then
  redis.call('PEXPIRE', KEYS[2], ARGV[2])
end
if count > tonumber(ARGV[1]) then
  local retry = redis.call('PTTL', KEYS[2])
  if tonumber(ARGV[3]) > 0 then
    redis.call('SET', KEYS[1], 'rate_limit', 'PX', ARGV[3])
    retry = tonumber(ARGV[3])
  end
  return {0, retry}
end
return {1, 0}
`)

var redisConsumeChallengeScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) then
  redis.call('DEL', KEYS[1])
  return 1
end
return 0
`)

func (s *redisStore) Allow(ctx context.Context, siteID, ruleID, value string, rule policy.RateLimitRule) (bool, time.Duration, error) {
	window := time.Duration(rule.WindowSeconds) * time.Second
	result, err := redisRateScript.Run(ctx, s.client,
		[]string{securitystate.RateBlockKey(siteID, ruleID, value), securitystate.RateCounterKey(siteID, ruleID, value)},
		rule.Requests+rule.Burst, window.Milliseconds(), (time.Duration(rule.BanSeconds) * time.Second).Milliseconds(),
	).Int64Slice()
	if err != nil {
		return false, 0, err
	}
	if len(result) != 2 {
		return false, 0, errors.New("unexpected Redis rate-limit response")
	}
	return result[0] == 1, time.Duration(max(result[1], 0)) * time.Millisecond, nil
}

func (s *redisStore) Blocked(ctx context.Context, siteID, address string) (bool, time.Duration, error) {
	ip, err := parseAddress(address)
	if err != nil {
		return false, 0, nil
	}
	pipe := s.client.Pipeline()
	global := pipe.PTTL(ctx, securitystate.GlobalBlockKey(ip))
	site := pipe.PTTL(ctx, securitystate.SiteBlockKey(siteID, ip))
	if _, err = pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return false, 0, err
	}
	retry := max(global.Val(), site.Val())
	return retry > 0, retry, nil
}

func (s *redisStore) PutChallenge(ctx context.Context, token string, ttl time.Duration) error {
	return s.client.Set(ctx, securitystate.ChallengeKey(token), "issued", ttl).Err()
}

func (s *redisStore) ConsumeChallenge(ctx context.Context, token string) (bool, error) {
	consumed, err := redisConsumeChallengeScript.Run(ctx, s.client, []string{securitystate.ChallengeKey(token)}).Int()
	return consumed == 1, err
}

var redisClients struct {
	sync.Mutex
	values map[string]*redis.Client
}

func configuredRedisStore() (distributedStore, error) {
	rawURL := os.Getenv("EDGE_AGENT_REDIS_URL")
	if rawURL == "" {
		return nil, errors.New("EDGE_AGENT_REDIS_URL is not configured")
	}
	redisClients.Lock()
	defer redisClients.Unlock()
	if redisClients.values == nil {
		redisClients.values = map[string]*redis.Client{}
	}
	if client := redisClients.values[rawURL]; client != nil {
		return &redisStore{client: client}, nil
	}
	options, err := redis.ParseURL(rawURL)
	if err != nil {
		return nil, err
	}
	options.MaxRetries = 1
	options.DialTimeout = time.Second
	options.ReadTimeout = time.Second
	options.WriteTimeout = time.Second
	client := redis.NewClient(options)
	redisClients.values[rawURL] = client
	return &redisStore{client: client}, nil
}

// redisProbeCacheTTL bounds how often agents ping Redis. Heartbeats are more
// frequent than this, so caching keeps hello/heartbeat from hammering the backend.
const redisProbeCacheTTL = 30 * time.Second

var redisProbeCache struct {
	sync.Mutex
	url       string
	at        time.Time
	available bool
	err       string
}

// CheckRedisBackend probes the distributed state backend. It reports
// available=true only when EDGE_AGENT_REDIS_URL is configured and Redis
// answers a ping, so the control plane can distinguish "agent never
// reported" from a genuine backend outage. Results are cached briefly so
// frequent heartbeats reuse the last probe.
func CheckRedisBackend(ctx context.Context) (available bool, statusError string) {
	rawURL := os.Getenv("EDGE_AGENT_REDIS_URL")
	now := time.Now()

	redisProbeCache.Lock()
	if redisProbeCache.url == rawURL && !redisProbeCache.at.IsZero() && now.Sub(redisProbeCache.at) < redisProbeCacheTTL {
		available, statusError = redisProbeCache.available, redisProbeCache.err
		redisProbeCache.Unlock()
		return available, statusError
	}
	redisProbeCache.Unlock()

	available, statusError = probeRedisBackend(ctx, rawURL)

	redisProbeCache.Lock()
	redisProbeCache.url = rawURL
	redisProbeCache.at = time.Now()
	redisProbeCache.available = available
	redisProbeCache.err = statusError
	redisProbeCache.Unlock()
	return available, statusError
}

func probeRedisBackend(ctx context.Context, rawURL string) (bool, string) {
	if rawURL == "" {
		return false, "EDGE_AGENT_REDIS_URL is not configured"
	}
	store, err := configuredRedisStore()
	if err != nil {
		return false, err.Error()
	}
	backend, ok := store.(*redisStore)
	if !ok {
		return false, "unexpected distributed store implementation"
	}
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := backend.client.Ping(pingCtx).Err(); err != nil {
		return false, "redis ping failed: " + err.Error()
	}
	return true, ""
}
