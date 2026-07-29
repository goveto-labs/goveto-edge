package analytics

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	db           *pgxpool.Pool
	live         *LiveBroker
	queryTimeout time.Duration
}

func NewStore(db *pgxpool.Pool, queryTimeout time.Duration) *Store {
	if queryTimeout <= 0 {
		queryTimeout = 5 * time.Second
	}
	return &Store{db: db, live: NewLiveBroker(), queryTimeout: queryTimeout}
}

func (s *Store) Ping(ctx context.Context) error { return s.db.Ping(ctx) }

func (s *Store) Ready(ctx context.Context) error {
	var ready bool
	err := s.db.QueryRow(ctx, `SELECT
		EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'timescaledb' AND extversion LIKE '2.%')
		AND to_regclass('analytics.web_request_logs') IS NOT NULL`).Scan(&ready)
	if err != nil {
		return err
	}
	if !ready {
		return fmt.Errorf("analytics schema is unavailable")
	}
	return nil
}

func (s *Store) queryContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, s.queryTimeout)
}

func (s *Store) Subscribe(ctx context.Context, filter LiveFilter, buffer int) <-chan LiveRequestLog {
	return s.live.Subscribe(ctx, filter, buffer)
}

func (s *Store) publish(events []WebRequestLog) { s.live.Publish(events) }

type continuousAggregateRefreshPolicy struct {
	view             string
	endOffset        string
	scheduleInterval string
}

var continuousAggregateRefreshPolicies = []continuousAggregateRefreshPolicy{
	{view: "analytics.request_usage_hourly", endOffset: "5 minutes", scheduleInterval: "5 minutes"},
	{view: "analytics.request_method_hourly", endOffset: "5 minutes", scheduleInterval: "5 minutes"},
	{view: "analytics.request_status_hourly", endOffset: "5 minutes", scheduleInterval: "5 minutes"},
	{view: "analytics.request_extension_hourly", endOffset: "5 minutes", scheduleInterval: "5 minutes"},
	{view: "analytics.request_hostname_hourly", endOffset: "5 minutes", scheduleInterval: "5 minutes"},
	{view: "analytics.request_referer_hourly", endOffset: "5 minutes", scheduleInterval: "5 minutes"},
	{view: "analytics.request_path_hourly", endOffset: "5 minutes", scheduleInterval: "5 minutes"},
	{view: "analytics.request_client_ip_hourly", endOffset: "5 minutes", scheduleInterval: "5 minutes"},
	{view: "analytics.request_usage_daily", endOffset: "1 hour", scheduleInterval: "1 hour"},
	{view: "analytics.request_method_daily", endOffset: "1 hour", scheduleInterval: "1 hour"},
	{view: "analytics.request_status_daily", endOffset: "1 hour", scheduleInterval: "1 hour"},
	{view: "analytics.request_extension_daily", endOffset: "1 hour", scheduleInterval: "1 hour"},
	{view: "analytics.request_hostname_daily", endOffset: "1 hour", scheduleInterval: "1 hour"},
	{view: "analytics.request_referer_daily", endOffset: "1 hour", scheduleInterval: "1 hour"},
	{view: "analytics.request_path_daily", endOffset: "1 hour", scheduleInterval: "1 hour"},
	{view: "analytics.request_client_ip_daily", endOffset: "1 hour", scheduleInterval: "1 hour"},
	{view: "analytics.node_traffic_metrics_minute", endOffset: "1 minute", scheduleInterval: "1 minute"},
}

func (s *Store) ConfigureRawRetention(ctx context.Context, days int) error {
	if days < 1 || days > 3650 {
		return fmt.Errorf("raw analytics retention days must be between 1 and 3650")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin analytics retention configuration: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(0x47434f524d52544e)); err != nil {
		return fmt.Errorf("lock analytics retention configuration: %w", err)
	}
	if _, err = tx.Exec(ctx, `SELECT remove_retention_policy(
		'analytics.web_request_logs', if_exists => TRUE)`); err != nil {
		return fmt.Errorf("remove analytics retention policy: %w", err)
	}
	if _, err = tx.Exec(ctx, `SELECT add_retention_policy(
		'analytics.web_request_logs', make_interval(days => $1), if_not_exists => TRUE)`, days); err != nil {
		return fmt.Errorf("configure analytics retention policy: %w", err)
	}
	for _, policy := range continuousAggregateRefreshPolicies {
		if _, err = tx.Exec(ctx, `SELECT remove_continuous_aggregate_policy(
			$1::regclass, if_exists => TRUE)`, policy.view); err != nil {
			return fmt.Errorf("remove refresh policy for %s: %w", policy.view, err)
		}
		if _, err = tx.Exec(ctx, `SELECT add_continuous_aggregate_policy(
			$1::regclass,
			start_offset => make_interval(days => $2),
			end_offset => $3::interval,
			schedule_interval => $4::interval,
			if_not_exists => TRUE)`, policy.view, days, policy.endOffset, policy.scheduleInterval); err != nil {
			return fmt.Errorf("configure refresh policy for %s: %w", policy.view, err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit analytics retention configuration: %w", err)
	}
	return nil
}

func (s *Store) Insert(ctx context.Context, events []WebRequestLog) ([]WebRequestLog, error) {
	const maxInsertBatch = 2000
	inserted := make([]WebRequestLog, 0, len(events))
	for start := 0; start < len(events); start += maxInsertBatch {
		end := min(start+maxInsertBatch, len(events))
		batch, err := s.insert(ctx, events[start:end])
		if err != nil {
			return nil, err
		}
		inserted = append(inserted, batch...)
	}
	return inserted, nil
}

var webRequestLogColumns = []string{
	"event_time", "source_log_id", "request_id", "cluster_id", "node_id", "site_id", "config_version",
	"hostname", "method", "scheme", "protocol", "path", "query_string", "client_ip", "status_code",
	"request_header_bytes", "request_body_bytes", "response_header_bytes", "response_body_bytes", "duration_us",
	"upstream_address", "upstream_status", "cache_status", "content_type", "file_extension", "referer",
	"user_agent", "country", "region", "waf_action", "waf_rule_id", "waf_source", "waf_match", "waf_tags",
}

func (s *Store) insert(ctx context.Context, events []WebRequestLog) ([]WebRequestLog, error) {
	if len(events) == 0 {
		return nil, nil
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `CREATE TEMP TABLE analytics_ingest_stage
		(LIKE analytics.web_request_logs INCLUDING DEFAULTS) ON COMMIT DROP`); err != nil {
		return nil, fmt.Errorf("create analytics staging table: %w", err)
	}
	rows := make([][]any, 0, len(events))
	for _, e := range events {
		rows = append(rows, []any{
			e.EventTime, e.SourceLogID, e.RequestID, e.ClusterID, e.NodeID, e.SiteID, e.ConfigVersion,
			e.Hostname, e.Method, e.Scheme, e.Protocol, e.Path, e.QueryString, e.ClientIP, e.StatusCode,
			e.RequestHeaderBytes, e.RequestBodyBytes, e.ResponseHeaderBytes, e.ResponseBodyBytes,
			e.Duration.Microseconds(), e.UpstreamAddress, e.UpstreamStatus, e.CacheStatus, e.ContentType,
			e.FileExtension, e.Referer, e.UserAgent, e.Country, e.Region, e.WAFAction, e.WAFRuleID,
			e.WAFSource, e.WAFMatch, e.WAFTags,
		})
	}
	if _, err = tx.CopyFrom(ctx, pgx.Identifier{"analytics_ingest_stage"}, webRequestLogColumns, pgx.CopyFromRows(rows)); err != nil {
		return nil, fmt.Errorf("copy analytics staging batch: %w", err)
	}
	insertSQL := `INSERT INTO analytics.web_request_logs (` + joinColumns(webRequestLogColumns) + `)
		SELECT ` + joinColumns(webRequestLogColumns) + ` FROM analytics_ingest_stage
		ON CONFLICT (event_time, node_id, source_log_id) DO NOTHING
		RETURNING event_time, node_id::text, source_log_id`
	returned, err := tx.Query(ctx, insertSQL)
	if err != nil {
		return nil, fmt.Errorf("merge analytics staging batch: %w", err)
	}
	type sourceKey struct {
		node string
		id   uint64
	}
	insertedKeys := make(map[sourceKey]struct{}, len(events))
	for returned.Next() {
		var key sourceKey
		var eventTime time.Time
		if err := returned.Scan(&eventTime, &key.node, &key.id); err != nil {
			returned.Close()
			return nil, err
		}
		insertedKeys[key] = struct{}{}
	}
	if err := returned.Err(); err != nil {
		returned.Close()
		return nil, err
	}
	returned.Close()
	if _, err = tx.Exec(ctx, `INSERT INTO analytics.daily_unique_ips (day, cluster_id, site_id, client_ip)
		SELECT DISTINCT (event_time AT TIME ZONE 'UTC')::date, cluster_id, site_id, client_ip FROM analytics_ingest_stage
		ON CONFLICT DO NOTHING`); err != nil {
		return nil, fmt.Errorf("merge analytics unique IPs: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit analytics batch: %w", err)
	}
	inserted := make([]WebRequestLog, 0, len(insertedKeys))
	for _, event := range events {
		if _, ok := insertedKeys[sourceKey{event.NodeID, event.SourceLogID}]; ok {
			inserted = append(inserted, event)
		}
	}
	return inserted, nil
}

func joinColumns(columns []string) string {
	result := ""
	for index, column := range columns {
		if index > 0 {
			result += ", "
		}
		result += column
	}
	return result
}

type NodeRuntimeMetric struct {
	Minute                                                time.Time
	ClusterID, NodeID                                     string
	CPU                                                   float32
	MemoryUsed, MemoryTotal                               uint64
	Load1, Load5, Load15                                  float32
	Connections                                           uint64
	CacheUsed                                             uint64
	CacheDirectory                                        string
	CacheEntries, CacheHits, CacheMisses, CacheStaleHits  uint64
	CacheEvictions, CacheRejectedWrites, CacheCorruptions uint64
	CacheHitRate, CacheCapacityRatio                      float32
	CacheAlerts                                           []string
	DiskUsed, DiskTotal                                   uint64
}

type OriginHealthMetric struct {
	Minute                      time.Time
	ClusterID, NodeID, SiteID   string
	OriginAddress               string
	Healthy, Available          bool
	Fails                       int
	Requests, Errors            uint64
	AverageLatencyMS, ErrorRate float64
}

func (s *Store) InsertOriginHealth(ctx context.Context, m OriginHealthMetric) error {
	_, err := s.db.Exec(ctx, `INSERT INTO analytics.origin_health_metrics_minute (
		minute, cluster_id, node_id, site_id, origin_address, healthy, available,
		fails, requests, errors, average_latency_ms, error_rate
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
	ON CONFLICT (minute, node_id, site_id, origin_address) DO UPDATE SET
		cluster_id=EXCLUDED.cluster_id, healthy=EXCLUDED.healthy, available=EXCLUDED.available,
		fails=EXCLUDED.fails, requests=EXCLUDED.requests, errors=EXCLUDED.errors,
		average_latency_ms=EXCLUDED.average_latency_ms, error_rate=EXCLUDED.error_rate`,
		m.Minute, m.ClusterID, m.NodeID, m.SiteID, m.OriginAddress, m.Healthy, m.Available,
		m.Fails, m.Requests, m.Errors, m.AverageLatencyMS, m.ErrorRate)
	return err
}

func (s *Store) InsertRuntime(ctx context.Context, m NodeRuntimeMetric) error {
	_, err := s.db.Exec(ctx, `INSERT INTO analytics.node_runtime_metrics_minute (
		minute, cluster_id, node_id, cpu_usage_percent, memory_used_bytes, memory_total_bytes,
		load_1, load_5, load_15, connections, cache_used_bytes, cache_directory,
		cache_entries, cache_hits, cache_misses, cache_stale_hits, cache_evictions,
		cache_rejected_writes, cache_corruptions, cache_hit_rate, cache_capacity_ratio,
		cache_alerts, disk_used_bytes, disk_total_bytes
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24)
	ON CONFLICT (minute, node_id) DO UPDATE SET
		cluster_id=EXCLUDED.cluster_id, cpu_usage_percent=EXCLUDED.cpu_usage_percent,
		memory_used_bytes=EXCLUDED.memory_used_bytes, memory_total_bytes=EXCLUDED.memory_total_bytes,
		load_1=EXCLUDED.load_1, load_5=EXCLUDED.load_5, load_15=EXCLUDED.load_15,
		connections=EXCLUDED.connections, cache_used_bytes=EXCLUDED.cache_used_bytes,
		cache_directory=EXCLUDED.cache_directory, cache_entries=EXCLUDED.cache_entries,
		cache_hits=EXCLUDED.cache_hits, cache_misses=EXCLUDED.cache_misses,
		cache_stale_hits=EXCLUDED.cache_stale_hits, cache_evictions=EXCLUDED.cache_evictions,
		cache_rejected_writes=EXCLUDED.cache_rejected_writes, cache_corruptions=EXCLUDED.cache_corruptions,
		cache_hit_rate=EXCLUDED.cache_hit_rate, cache_capacity_ratio=EXCLUDED.cache_capacity_ratio,
		cache_alerts=EXCLUDED.cache_alerts, disk_used_bytes=EXCLUDED.disk_used_bytes,
		disk_total_bytes=EXCLUDED.disk_total_bytes`,
		m.Minute, m.ClusterID, m.NodeID, m.CPU, m.MemoryUsed, m.MemoryTotal, m.Load1, m.Load5,
		m.Load15, m.Connections, m.CacheUsed, m.CacheDirectory, m.CacheEntries, m.CacheHits,
		m.CacheMisses, m.CacheStaleHits, m.CacheEvictions, m.CacheRejectedWrites, m.CacheCorruptions,
		m.CacheHitRate, m.CacheCapacityRatio, m.CacheAlerts, m.DiskUsed, m.DiskTotal)
	return err
}
