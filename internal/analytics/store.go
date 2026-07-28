package analytics

import (
	"context"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
)

type Store struct {
	db   clickhouse.Conn
	live *LiveBroker
}

func NewStore(db clickhouse.Conn) *Store {
	return &Store{db: db, live: NewLiveBroker()}
}

func (s *Store) Subscribe(ctx context.Context, filter LiveFilter, buffer int) <-chan LiveRequestLog {
	return s.live.Subscribe(ctx, filter, buffer)
}

func (s *Store) publish(events []WebRequestLog) { s.live.Publish(events) }

func (s *Store) ConfigureRawRetention(ctx context.Context, days int) error {
	if days < 1 || days > 3650 {
		return fmt.Errorf("raw analytics retention days must be between 1 and 3650")
	}
	return s.db.Exec(ctx, fmt.Sprintf(
		"ALTER TABLE goveto.web_request_logs MODIFY TTL event_time + INTERVAL %d DAY DELETE",
		days,
	))
}

func (s *Store) Insert(ctx context.Context, events []WebRequestLog) error {
	const maxInsertBatch = 2000
	for start := 0; start < len(events); start += maxInsertBatch {
		end := min(start+maxInsertBatch, len(events))
		if err := s.insert(ctx, events[start:end]); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) insert(ctx context.Context, events []WebRequestLog) error {
	if len(events) == 0 {
		return nil
	}

	b, err := s.db.PrepareBatch(ctx, `INSERT INTO goveto.web_request_logs (
		event_time,
		request_id,
		cluster_id,
		node_id,
		site_id,
		config_version,
		hostname,
		method,
		scheme,
		protocol,
		path,
		query_string,
		client_ip,
		status_code,
		request_header_bytes,
		request_body_bytes,
		response_header_bytes,
		response_body_bytes,
		duration_us,
		upstream_address,
		upstream_status,
		cache_status,
		content_type,
		file_extension,
		referer,
		user_agent,
		country,
		region,
		waf_action,
		waf_rule_id,
		waf_source,
		waf_match,
		waf_tags
	)`)
	if err != nil {
		return err
	}

	for _, e := range events {
		if err = b.Append(
			e.EventTime,
			e.RequestID,
			e.ClusterID,
			e.NodeID,
			e.SiteID,
			e.ConfigVersion,
			e.Hostname,
			e.Method,
			e.Scheme,
			e.Protocol,
			e.Path,
			e.QueryString,
			e.ClientIP,
			e.StatusCode,
			e.RequestHeaderBytes,
			e.RequestBodyBytes,
			e.ResponseHeaderBytes,
			e.ResponseBodyBytes,
			uint64(e.Duration.Microseconds()),
			e.UpstreamAddress,
			e.UpstreamStatus,
			e.CacheStatus,
			e.ContentType,
			e.FileExtension,
			e.Referer,
			e.UserAgent,
			e.Country,
			e.Region,
			e.WAFAction,
			e.WAFRuleID,
			e.WAFSource,
			e.WAFMatch,
			e.WAFTags,
		); err != nil {
			return err
		}
	}

	return b.Send()
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
	return s.db.Exec(ctx, `INSERT INTO goveto.origin_health_metrics_minute (
		minute, cluster_id, node_id, site_id, origin_address, healthy, available,
		fails, requests, errors, average_latency_ms, error_rate
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		m.Minute, m.ClusterID, m.NodeID, m.SiteID, m.OriginAddress, m.Healthy, m.Available,
		m.Fails, m.Requests, m.Errors, m.AverageLatencyMS, m.ErrorRate,
	)
}

func (s *Store) InsertRuntime(ctx context.Context, m NodeRuntimeMetric) error {
	return s.db.Exec(ctx, `INSERT INTO goveto.node_runtime_metrics_minute (
		minute,
		cluster_id,
		node_id,
		cpu_usage_percent,
		memory_used_bytes,
		memory_total_bytes,
		load_1,
		load_5,
		load_15,
		connections,
		cache_used_bytes,
		cache_directory,
		cache_entries, cache_hits, cache_misses, cache_stale_hits,
		cache_evictions, cache_rejected_writes, cache_corruptions,
		cache_hit_rate, cache_capacity_ratio, cache_alerts,
		disk_used_bytes,
		disk_total_bytes
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		m.Minute,
		m.ClusterID,
		m.NodeID,
		m.CPU,
		m.MemoryUsed,
		m.MemoryTotal,
		m.Load1,
		m.Load5,
		m.Load15,
		m.Connections,
		m.CacheUsed,
		m.CacheDirectory,
		m.CacheEntries,
		m.CacheHits,
		m.CacheMisses,
		m.CacheStaleHits,
		m.CacheEvictions,
		m.CacheRejectedWrites,
		m.CacheCorruptions,
		m.CacheHitRate,
		m.CacheCapacityRatio,
		m.CacheAlerts,
		m.DiskUsed,
		m.DiskTotal,
	)
}
