package waf

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"goveto-edge/internal/policy"
	"goveto-edge/internal/securitystate"
)

// autoBanStore records WAF group hits and reads back the temporary blocks
// those hits produce. The Redis implementation shares the same block keys
// as the control-plane /security/blocks API and access temporary blocks, so
// a WAF-driven ban is indistinguishable from an operator-issued one. The
// local implementation keeps an in-memory counter and ban map for agents
// without a Redis backend.
type autoBanStore interface {
	RecordHit(ctx context.Context, siteID, groupID, address string, cfg policy.WAFAutoBan) (banned bool, retry time.Duration, err error)
	Blocked(ctx context.Context, siteID, address string) (bool, time.Duration, error)
}

// redisScriptMillisecondDuration converts millisecond integers returned by
// Lua scripts (where go-redis does not auto-scale) into time.Duration.
func redisScriptMillisecondDuration(ms int64) time.Duration {
	if ms <= 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}

// redisAutoBanScript increments the per-(site,group,ip) hit counter, expires
// it on the first hit, and when the threshold is reached writes the block key
// (site-scoped or global) with the ban TTL. Returns {banned, retryMs}.
var redisAutoBanScript = redis.NewScript(`
local count = redis.call('INCR', KEYS[1])
if count == 1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
if count >= tonumber(ARGV[2]) then
  local ban_ttl = tonumber(ARGV[3])
  local existing_ttl = redis.call('PTTL', KEYS[2])
  if existing_ttl == -2 then
    redis.call('SET', KEYS[2], ARGV[4], 'PX', ban_ttl)
  elseif existing_ttl >= 0 and existing_ttl < ban_ttl then
    local existing_value = redis.call('GET', KEYS[2])
    local decoded, record = pcall(cjson.decode, existing_value)
    if decoded and type(record) == 'table' then
      record.expires_at = ARGV[5]
      redis.call('SET', KEYS[2], cjson.encode(record), 'PX', ban_ttl)
    else
      redis.call('PEXPIRE', KEYS[2], ban_ttl)
    end
  end
  local retry = redis.call('PTTL', KEYS[2])
  return {1, retry}
end
local retry = redis.call('PTTL', KEYS[1])
if retry < 0 then
  retry = ARGV[1]
end
return {0, retry}
`)

type redisAutoBanStore struct{ client *redis.Client }

func autoBanBlockValue(siteID, groupID string, ip string, cfg policy.WAFAutoBan, now time.Time) string {
	record := securitystate.TemporaryBlock{
		Scope: cfg.Scope, Address: ip, Reason: fmt.Sprintf("WAF auto-ban group %s", groupID),
		CreatedBy: "waf:auto_ban", CreatedAt: now.UTC(),
		ExpiresAt: now.UTC().Add(time.Duration(cfg.BanSeconds) * time.Second),
	}
	if cfg.Scope == "SITE" {
		record.SiteID = siteID
	}
	encoded, _ := json.Marshal(record)
	return string(encoded)
}

func (s *redisAutoBanStore) RecordHit(ctx context.Context, siteID, groupID, address string, cfg policy.WAFAutoBan) (bool, time.Duration, error) {
	ip, err := parseAddress(address)
	if err != nil {
		return false, 0, nil
	}
	window := (time.Duration(cfg.WindowSeconds) * time.Second).Milliseconds()
	banTTL := (time.Duration(cfg.BanSeconds) * time.Second).Milliseconds()
	var blockKey string
	if cfg.Scope == "GLOBAL" {
		blockKey = securitystate.GlobalBlockKey(ip)
	} else {
		blockKey = securitystate.SiteBlockKey(siteID, ip)
	}
	now := time.Now()
	expiresAt := now.Add(time.Duration(cfg.BanSeconds) * time.Second).UTC().Format(time.RFC3339Nano)
	result, err := redisAutoBanScript.Run(ctx, s.client,
		[]string{securitystate.WAFAutoBanCounterKey(siteID, groupID, ip), blockKey},
		window, cfg.Hits, banTTL, autoBanBlockValue(siteID, groupID, ip.String(), cfg, now), expiresAt,
	).Int64Slice()
	if err != nil {
		return false, 0, err
	}
	if len(result) != 2 {
		return false, 0, errors.New("unexpected WAF auto-ban response")
	}
	return result[0] == 1, redisScriptMillisecondDuration(result[1]), nil
}

func (s *redisAutoBanStore) Blocked(ctx context.Context, siteID, address string) (bool, time.Duration, error) {
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
	// go-redis PTTL returns a DurationCmd already scaled to time.Duration.
	retry := global.Val()
	if site.Val() > retry {
		retry = site.Val()
	}
	return retry > 0, retry, nil
}

// localAutoBanMaxKeys caps in-memory counters during Redis-less operation so a
// flood of distinct client IPs cannot grow process memory without bound.
const localAutoBanMaxKeys = 4096

// localAutoBanStore is an in-memory auto-ban store for agents that do not run
// against Redis. Bans are per-process, mirroring the local rate-limit counter.
type localAutoBanStore struct {
	mu     sync.Mutex
	counts map[string]localAutoBanCounter
	bans   map[string]time.Time
}

type localAutoBanCounter struct {
	windowStart time.Time
	window      time.Duration
	count       int
	lastSeen    time.Time
}

func newLocalAutoBanStore() *localAutoBanStore {
	return &localAutoBanStore{counts: map[string]localAutoBanCounter{}, bans: map[string]time.Time{}}
}

// processLocalAutoBanStore is shared by every site handler in this process so
// GLOBAL bans retain their documented meaning when Redis is not configured.
var processLocalAutoBanStore = newLocalAutoBanStore()

func (s *localAutoBanStore) evictLocked(now time.Time) {
	for key, until := range s.bans {
		if !until.After(now) {
			delete(s.bans, key)
		}
	}
	for key, entry := range s.counts {
		if entry.window > 0 && !entry.windowStart.IsZero() && now.Sub(entry.windowStart) >= entry.window {
			delete(s.counts, key)
		}
	}
	for len(s.counts) >= localAutoBanMaxKeys {
		var oldestKey string
		var oldest time.Time
		for key, entry := range s.counts {
			if oldestKey == "" || entry.lastSeen.Before(oldest) {
				oldestKey = key
				oldest = entry.lastSeen
			}
		}
		if oldestKey == "" {
			break
		}
		delete(s.counts, oldestKey)
	}
}

func (s *localAutoBanStore) RecordHit(_ context.Context, siteID, groupID, address string, cfg policy.WAFAutoBan) (bool, time.Duration, error) {
	ip, err := parseAddress(address)
	if err != nil {
		return false, 0, nil
	}
	key := siteID + "\x00" + groupID + "\x00" + ip.String()
	now := time.Now()
	window := time.Duration(cfg.WindowSeconds) * time.Second
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evictLocked(now)
	entry, exists := s.counts[key]
	if !exists && len(s.counts) >= localAutoBanMaxKeys {
		// At capacity after eviction: drop this hit rather than growing RAM.
		return false, window, nil
	}
	entry.lastSeen = now
	entry.window = window
	if entry.windowStart.IsZero() || now.Sub(entry.windowStart) >= window {
		entry.windowStart, entry.count = now, 0
	}
	entry.count++
	if entry.count >= cfg.Hits {
		banUntil := now.Add(time.Duration(cfg.BanSeconds) * time.Second)
		var blockKey string
		if cfg.Scope == "GLOBAL" {
			blockKey = securitystate.GlobalBlockKey(ip)
		} else {
			blockKey = securitystate.SiteBlockKey(siteID, ip)
		}
		if existing := s.bans[blockKey]; existing.After(banUntil) {
			banUntil = existing
		}
		s.bans[blockKey] = banUntil
		delete(s.counts, key)
		return true, banUntil.Sub(now), nil
	}
	s.counts[key] = entry
	return false, window - now.Sub(entry.windowStart), nil
}

func (s *localAutoBanStore) Blocked(_ context.Context, siteID, address string) (bool, time.Duration, error) {
	ip, err := parseAddress(address)
	if err != nil {
		return false, 0, nil
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evictLocked(now)
	retry := time.Duration(0)
	if until, ok := s.bans[securitystate.GlobalBlockKey(ip)]; ok && until.After(now) {
		retry = until.Sub(now)
	}
	if until, ok := s.bans[securitystate.SiteBlockKey(siteID, ip)]; ok && until.After(now) {
		if r := until.Sub(now); r > retry {
			retry = r
		}
	}
	return retry > 0, retry, nil
}
