package analytics

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var ErrInvalidDimension = errors.New("invalid analytics dimension")

type TrafficPoint struct {
	Bucket           time.Time `json:"bucket"`
	Requests         uint64    `json:"requests"`
	IngressBytes     uint64    `json:"ingress_bytes"`
	EgressBytes      uint64    `json:"egress_bytes"`
	CacheEgressBytes uint64    `json:"cache_egress_bytes"`
}

type DistributionItem struct {
	Value        string `json:"value"`
	Requests     uint64 `json:"requests"`
	IngressBytes uint64 `json:"ingress_bytes"`
	EgressBytes  uint64 `json:"egress_bytes"`
}

type NodeRuntimePoint struct {
	Bucket              time.Time `json:"bucket"`
	NodeID              string    `json:"node_id"`
	CPU                 float32   `json:"cpu_usage_percent"`
	MemoryUsed          uint64    `json:"memory_used_bytes"`
	MemoryTotal         uint64    `json:"memory_total_bytes"`
	Load1               float32   `json:"load_1"`
	Load5               float32   `json:"load_5"`
	Load15              float32   `json:"load_15"`
	Connections         uint64    `json:"connections"`
	CacheUsed           uint64    `json:"cache_used_bytes"`
	CacheDirectory      string    `json:"cache_directory"`
	CacheEntries        uint64    `json:"cache_entries"`
	CacheHits           uint64    `json:"cache_hits"`
	CacheMisses         uint64    `json:"cache_misses"`
	CacheStaleHits      uint64    `json:"cache_stale_hits"`
	CacheEvictions      uint64    `json:"cache_evictions"`
	CacheRejectedWrites uint64    `json:"cache_rejected_writes"`
	CacheCorruptions    uint64    `json:"cache_corruptions"`
	CacheHitRate        float32   `json:"cache_hit_rate"`
	CacheCapacityRatio  float32   `json:"cache_capacity_ratio"`
	CacheAlerts         []string  `json:"cache_alerts"`
	DiskUsed            uint64    `json:"disk_used_bytes"`
	DiskTotal           uint64    `json:"disk_total_bytes"`
}

type NodeSnapshot struct {
	NodeRuntimePoint
	Online                    bool    `json:"online"`
	IngressBytesPerSecond     float64 `json:"ingress_bytes_per_second"`
	EgressBytesPerSecond      float64 `json:"egress_bytes_per_second"`
	CacheEgressBytesPerSecond float64 `json:"cache_egress_bytes_per_second"`
	RequestsPerMinute         uint64  `json:"requests_per_minute"`
}

const runtimeColumns = `minute, node_id::text, cpu_usage_percent, memory_used_bytes,
	memory_total_bytes, load_1, load_5, load_15, connections, cache_used_bytes,
	cache_directory, cache_entries, cache_hits, cache_misses, cache_stale_hits,
	cache_evictions, cache_rejected_writes, cache_corruptions, cache_hit_rate,
	cache_capacity_ratio, cache_alerts, disk_used_bytes, disk_total_bytes`

func scanRuntime(row interface{ Scan(...any) error }, x *NodeRuntimePoint) error {
	return row.Scan(&x.Bucket, &x.NodeID, &x.CPU, &x.MemoryUsed, &x.MemoryTotal,
		&x.Load1, &x.Load5, &x.Load15, &x.Connections, &x.CacheUsed, &x.CacheDirectory,
		&x.CacheEntries, &x.CacheHits, &x.CacheMisses, &x.CacheStaleHits, &x.CacheEvictions,
		&x.CacheRejectedWrites, &x.CacheCorruptions, &x.CacheHitRate, &x.CacheCapacityRatio,
		&x.CacheAlerts, &x.DiskUsed, &x.DiskTotal)
}

func (s *Store) LatestNodeRuntime(ctx context.Context, cluster, nodeID string) ([]NodeSnapshot, error) {
	ctx, cancel := s.queryContext(ctx)
	defer cancel()
	q := `SELECT DISTINCT ON (node_id) ` + runtimeColumns + `
		FROM analytics.node_runtime_metrics_minute WHERE cluster_id = $1`
	args := []any{cluster}
	if nodeID != "" {
		q += " AND node_id = $2"
		args = append(args, nodeID)
	}
	q += " ORDER BY node_id, minute DESC"
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	points := make([]NodeRuntimePoint, 0)
	for rows.Next() {
		var point NodeRuntimePoint
		if err := scanRuntime(rows, &point); err != nil {
			return nil, err
		}
		points = append(points, point)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	traffic, err := s.latestNodeTraffic(ctx, cluster, nodeID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	out := make([]NodeSnapshot, 0, len(points))
	for _, point := range points {
		snapshot := NodeSnapshot{NodeRuntimePoint: point, Online: now.Sub(point.Bucket) <= 3*time.Minute}
		if current, ok := traffic[point.NodeID]; ok {
			snapshot.IngressBytesPerSecond = float64(current.IngressBytes) / 60
			snapshot.EgressBytesPerSecond = float64(current.EgressBytes) / 60
			snapshot.CacheEgressBytesPerSecond = float64(current.CacheEgressBytes) / 60
			snapshot.RequestsPerMinute = current.Requests
		}
		out = append(out, snapshot)
	}
	return out, nil
}

type nodeTrafficMinute struct {
	Requests, IngressBytes, EgressBytes, CacheEgressBytes uint64
}

func (s *Store) latestNodeTraffic(ctx context.Context, cluster, nodeID string) (map[string]nodeTrafficMinute, error) {
	q := `SELECT node_id::text, sum(requests)::bigint, sum(ingress_bytes)::bigint,
		sum(egress_bytes)::bigint, sum(cache_egress_bytes)::bigint
		FROM analytics.node_traffic_metrics_minute
		WHERE cluster_id = $1 AND minute >= now() - INTERVAL '1 minute'`
	args := []any{cluster}
	if nodeID != "" {
		q += " AND node_id = $2"
		args = append(args, nodeID)
	}
	q += " GROUP BY node_id"
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]nodeTrafficMinute{}
	for rows.Next() {
		var id string
		var current nodeTrafficMinute
		if err := rows.Scan(&id, &current.Requests, &current.IngressBytes, &current.EgressBytes, &current.CacheEgressBytes); err != nil {
			return nil, err
		}
		out[id] = current
	}
	return out, rows.Err()
}

func (s *Store) TrafficSeries(ctx context.Context, cluster, site, nodeID, period string) ([]TrafficPoint, error) {
	ctx, cancel := s.queryContext(ctx)
	defer cancel()
	now := time.Now().UTC()
	from := now.Add(-24 * time.Hour)
	fullStart, fullEnd := completeHourRange(from, now)
	args := []any{cluster, from, fullStart, fullEnd, now}
	filter := ""
	if site != "" {
		filter += fmt.Sprintf(" AND site_id = $%d", len(args)+1)
		args = append(args, site)
	}
	if nodeID != "" {
		filter += fmt.Sprintf(" AND node_id = $%d", len(args)+1)
		args = append(args, nodeID)
	}
	q := `SELECT bucket, sum(requests)::bigint, sum(ingress_bytes)::bigint,
		sum(egress_bytes)::bigint, sum(cache_egress_bytes)::bigint FROM (
		SELECT time_bucket(INTERVAL '1 hour', event_time) AS bucket, count(*)::bigint AS requests,
			sum(request_header_bytes + request_body_bytes)::bigint AS ingress_bytes,
			sum(response_header_bytes + response_body_bytes)::bigint AS egress_bytes,
			COALESCE(sum(response_header_bytes + response_body_bytes) FILTER (WHERE upper(cache_status) = 'HIT'), 0)::bigint AS cache_egress_bytes
		FROM analytics.web_request_logs
		WHERE cluster_id = $1 AND event_time >= $2 AND event_time < $3` + filter + ` GROUP BY 1
		UNION ALL
		SELECT bucket, requests, ingress_bytes, egress_bytes, cache_egress_bytes
		FROM analytics.request_usage_hourly
		WHERE cluster_id = $1 AND bucket >= $3 AND bucket < $4` + filter + `
		UNION ALL
		SELECT time_bucket(INTERVAL '1 hour', event_time), count(*)::bigint,
			sum(request_header_bytes + request_body_bytes)::bigint,
			sum(response_header_bytes + response_body_bytes)::bigint,
			COALESCE(sum(response_header_bytes + response_body_bytes) FILTER (WHERE upper(cache_status) = 'HIT'), 0)::bigint
		FROM analytics.web_request_logs
		WHERE cluster_id = $1 AND event_time >= $4 AND event_time < $5` + filter + ` GROUP BY 1
	) totals GROUP BY bucket ORDER BY bucket`
	if period == "30d" {
		from, today := utcDayRange(now, 30)
		args = []any{cluster, from, today}
		dailyFilter := analyticsDimensions(&args, site, nodeID)
		args = append(args, cluster, today, now)
		hourlyFilter := analyticsDimensions(&args, site, nodeID)
		q = `SELECT bucket, sum(requests)::bigint, sum(ingress_bytes)::bigint,
			sum(egress_bytes)::bigint, sum(cache_egress_bytes)::bigint FROM (
			SELECT bucket, requests, ingress_bytes, egress_bytes, cache_egress_bytes
			FROM analytics.request_usage_daily WHERE cluster_id = $1 AND bucket >= $2 AND bucket < $3` + dailyFilter + `
			UNION ALL
			SELECT time_bucket(INTERVAL '1 day', bucket), requests, ingress_bytes, egress_bytes, cache_egress_bytes
			FROM analytics.request_usage_hourly WHERE cluster_id = $` + fmt.Sprint(4+dimensionCount(site, nodeID)) +
			` AND bucket >= $` + fmt.Sprint(5+dimensionCount(site, nodeID)) + ` AND bucket < $` + fmt.Sprint(6+dimensionCount(site, nodeID)) + hourlyFilter + `
		) totals GROUP BY bucket ORDER BY bucket`
	}
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]TrafficPoint, 0)
	for rows.Next() {
		var point TrafficPoint
		if err := rows.Scan(&point.Bucket, &point.Requests, &point.IngressBytes, &point.EgressBytes, &point.CacheEgressBytes); err != nil {
			return nil, err
		}
		out = append(out, point)
	}
	return out, rows.Err()
}

func analyticsDimensions(args *[]any, site, nodeID string) string {
	filter := ""
	if site != "" {
		filter += fmt.Sprintf(" AND site_id = $%d", len(*args)+1)
		*args = append(*args, site)
	}
	if nodeID != "" {
		filter += fmt.Sprintf(" AND node_id = $%d", len(*args)+1)
		*args = append(*args, nodeID)
	}
	return filter
}

func dimensionCount(site, node string) int {
	count := 0
	if site != "" {
		count++
	}
	if node != "" {
		count++
	}
	return count
}

var dimensions = map[string]string{
	"extension": "file_extension", "file_extension": "file_extension",
	"hostname": "hostname", "domain": "hostname", "referer": "referer",
	"status": "status_code::text", "method": "method", "path": "path",
	"ip": "host(client_ip)", "country": "country", "region": "region", "node": "node_id::text",
}

func (s *Store) Ranking(ctx context.Context, cluster, site, nodeID, period, dimension, sortBy string, limit int) ([]DistributionItem, error) {
	ctx, cancel := s.queryContext(ctx)
	defer cancel()
	if dimension == "domain" {
		dimension = "hostname"
	}
	if dimension == "file_extension" {
		dimension = "extension"
	}
	column, ok := dimensions[dimension]
	if !ok {
		return nil, ErrInvalidDimension
	}
	if period == "30d" && dimension == "node" {
		return s.rankingNode30d(ctx, cluster, site, sortBy, limit)
	}
	if period == "30d" {
		return s.ranking30d(ctx, cluster, site, dimension, sortBy, limit)
	}
	if nodeID == "" {
		return s.ranking24h(ctx, cluster, site, dimension, column, sortBy, limit)
	}
	from := time.Now().UTC().Add(-24 * time.Hour)
	q := fmt.Sprintf(`SELECT %s AS value, count(*)::bigint,
		sum(request_header_bytes + request_body_bytes)::bigint,
		sum(response_header_bytes + response_body_bytes)::bigint
		FROM analytics.web_request_logs WHERE cluster_id = $1 AND event_time >= $2`, column)
	args := []any{cluster, from}
	if site != "" {
		q += fmt.Sprintf(" AND site_id = $%d", len(args)+1)
		args = append(args, site)
	}
	if nodeID != "" {
		q += fmt.Sprintf(" AND node_id = $%d", len(args)+1)
		args = append(args, nodeID)
	}
	q += " GROUP BY 1 ORDER BY "
	if sortBy == "traffic" {
		q += "(sum(request_header_bytes + request_body_bytes) + sum(response_header_bytes + response_body_bytes)) DESC"
	} else {
		q += "count(*) DESC"
	}
	q += fmt.Sprintf(" LIMIT $%d", len(args)+1)
	args = append(args, limit)
	return s.scanRanking(ctx, q, args)
}

func (s *Store) ranking24h(ctx context.Context, cluster, site, dimension, rawColumn, sortBy string, limit int) ([]DistributionItem, error) {
	now := time.Now().UTC()
	from := now.Add(-24 * time.Hour)
	fullStart, fullEnd := completeHourRange(from, now)
	view := map[string]string{
		"extension": "request_extension_hourly", "status": "request_status_hourly",
		"method": "request_method_hourly", "hostname": "request_hostname_hourly",
		"referer": "request_referer_hourly", "path": "request_path_hourly",
		"ip": "request_client_ip_hourly", "country": "request_country_hourly",
		"region": "request_region_hourly", "node": "request_usage_hourly",
	}[dimension]
	if view == "" {
		return nil, ErrInvalidDimension
	}
	aggregateValue := "value"
	if dimension == "node" {
		aggregateValue = "node_id::text"
	} else if dimension == "ip" {
		aggregateValue = "host(value)"
	} else if dimension == "status" {
		aggregateValue = "value::text"
	}
	args := []any{cluster, from, fullStart, fullEnd, now}
	siteFilter := ""
	if site != "" {
		siteFilter = fmt.Sprintf(" AND site_id = $%d", len(args)+1)
		args = append(args, site)
	}
	q := fmt.Sprintf(`SELECT value, sum(requests)::bigint, sum(ingress_bytes)::bigint,
		sum(egress_bytes)::bigint FROM (
		SELECT %s AS value, count(*)::bigint AS requests,
			sum(request_header_bytes + request_body_bytes)::bigint AS ingress_bytes,
			sum(response_header_bytes + response_body_bytes)::bigint AS egress_bytes
		FROM analytics.web_request_logs
		WHERE cluster_id = $1 AND event_time >= $2 AND event_time < $3%s GROUP BY 1
		UNION ALL
		SELECT %s AS value, requests, ingress_bytes, egress_bytes
		FROM analytics.%s
		WHERE cluster_id = $1 AND bucket >= $3 AND bucket < $4%s
		UNION ALL
		SELECT %s AS value, count(*)::bigint,
			sum(request_header_bytes + request_body_bytes)::bigint,
			sum(response_header_bytes + response_body_bytes)::bigint
		FROM analytics.web_request_logs
		WHERE cluster_id = $1 AND event_time >= $4 AND event_time < $5%s GROUP BY 1
	) ranking GROUP BY value ORDER BY `,
		rawColumn, siteFilter, aggregateValue, view, siteFilter, rawColumn, siteFilter)
	if sortBy == "traffic" {
		q += "sum(ingress_bytes + egress_bytes) DESC"
	} else {
		q += "sum(requests) DESC"
	}
	q += fmt.Sprintf(" LIMIT $%d", len(args)+1)
	args = append(args, limit)
	return s.scanRanking(ctx, q, args)
}

func (s *Store) rankingNode30d(ctx context.Context, cluster, site, sortBy string, limit int) ([]DistributionItem, error) {
	now := time.Now().UTC()
	from, today := utcDayRange(now, 30)
	args := []any{cluster, from, today}
	dailySite := ""
	if site != "" {
		dailySite = fmt.Sprintf(" AND site_id = $%d", len(args)+1)
		args = append(args, site)
	}
	args = append(args, cluster, today, now)
	hourlySite := ""
	if site != "" {
		hourlySite = fmt.Sprintf(" AND site_id = $%d", len(args)+1)
		args = append(args, site)
	}
	base := 4
	if site != "" {
		base++
	}
	q := fmt.Sprintf(`SELECT value, sum(requests)::bigint, sum(ingress_bytes)::bigint,
		sum(egress_bytes)::bigint FROM (
		SELECT node_id::text AS value, requests, ingress_bytes, egress_bytes
		FROM analytics.request_usage_daily
		WHERE cluster_id = $1 AND bucket >= $2 AND bucket < $3%s
		UNION ALL
		SELECT node_id::text AS value, requests, ingress_bytes, egress_bytes
		FROM analytics.request_usage_hourly
		WHERE cluster_id = $%d AND bucket >= $%d AND bucket < $%d%s
	) ranking GROUP BY value ORDER BY `, dailySite, base, base+1, base+2, hourlySite)
	if sortBy == "traffic" {
		q += "sum(ingress_bytes + egress_bytes) DESC"
	} else {
		q += "sum(requests) DESC"
	}
	q += fmt.Sprintf(" LIMIT $%d", len(args)+1)
	args = append(args, limit)
	return s.scanRanking(ctx, q, args)
}

func (s *Store) ranking30d(ctx context.Context, cluster, site, dimension, sortBy string, limit int) ([]DistributionItem, error) {
	view := map[string]string{"extension": "extension", "status": "status", "method": "method", "hostname": "hostname", "referer": "referer", "path": "path", "ip": "client_ip", "country": "country", "region": "region"}[dimension]
	if view == "" {
		return nil, ErrInvalidDimension
	}
	value := "value"
	if dimension == "ip" {
		value = "host(value)"
	} else if dimension == "status" {
		value = "value::text"
	}
	now := time.Now().UTC()
	from, today := utcDayRange(now, 30)
	args := []any{cluster, from, today}
	dailySite := ""
	if site != "" {
		dailySite = fmt.Sprintf(" AND site_id = $%d", len(args)+1)
		args = append(args, site)
	}
	args = append(args, cluster, today, now)
	hourlySite := ""
	if site != "" {
		hourlySite = fmt.Sprintf(" AND site_id = $%d", len(args)+1)
		args = append(args, site)
	}
	base := 4
	if site != "" {
		base++
	}
	q := fmt.Sprintf(`SELECT value, sum(requests)::bigint, sum(ingress_bytes)::bigint, sum(egress_bytes)::bigint FROM (
		SELECT %s AS value, requests, ingress_bytes, egress_bytes FROM analytics.request_%s_daily
		WHERE cluster_id = $1 AND bucket >= $2 AND bucket < $3%s
		UNION ALL
		SELECT %s AS value, requests, ingress_bytes, egress_bytes FROM analytics.request_%s_hourly
		WHERE cluster_id = $%d AND bucket >= $%d AND bucket < $%d%s
	) ranking GROUP BY value ORDER BY `, value, view, dailySite, value, view, base, base+1, base+2, hourlySite)
	if sortBy == "traffic" {
		q += "sum(ingress_bytes + egress_bytes) DESC"
	} else {
		q += "sum(requests) DESC"
	}
	q += fmt.Sprintf(" LIMIT $%d", len(args)+1)
	args = append(args, limit)
	return s.scanRanking(ctx, q, args)
}

func (s *Store) scanRanking(ctx context.Context, q string, args []any) ([]DistributionItem, error) {
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]DistributionItem, 0)
	for rows.Next() {
		var item DistributionItem
		if err := rows.Scan(&item.Value, &item.Requests, &item.IngressBytes, &item.EgressBytes); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) NodeRuntime(ctx context.Context, cluster, nodeID, period string) ([]NodeRuntimePoint, error) {
	ctx, cancel := s.queryContext(ctx)
	defer cancel()
	bucket := "time_bucket(INTERVAL '5 minutes', minute)"
	from := time.Now().UTC().Add(-12 * time.Hour)
	if period == "24h" {
		from = time.Now().UTC().Add(-24 * time.Hour)
	}
	if period == "30d" {
		bucket = "time_bucket(INTERVAL '1 hour', minute)"
		from = time.Now().UTC().AddDate(0, 0, -30)
	}
	q := `SELECT ` + bucket + `, node_id::text, avg(cpu_usage_percent)::real,
		avg(memory_used_bytes)::bigint, avg(memory_total_bytes)::bigint, avg(load_1)::real,
		avg(load_5)::real, avg(load_15)::real, avg(connections)::bigint,
		avg(cache_used_bytes)::bigint, last(cache_directory, minute), last(cache_entries, minute)::bigint,
		last(cache_hits, minute)::bigint, last(cache_misses, minute)::bigint,
		last(cache_stale_hits, minute)::bigint, last(cache_evictions, minute)::bigint,
		last(cache_rejected_writes, minute)::bigint, last(cache_corruptions, minute)::bigint,
		last(cache_hit_rate, minute)::real, avg(cache_capacity_ratio)::real,
		last(cache_alerts, minute), avg(disk_used_bytes)::bigint, avg(disk_total_bytes)::bigint
		FROM analytics.node_runtime_metrics_minute WHERE cluster_id = $1 AND minute >= $2`
	args := []any{cluster, from}
	if nodeID != "" {
		q += " AND node_id = $3"
		args = append(args, nodeID)
	}
	q += " GROUP BY 1, 2 ORDER BY 1, 2"
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]NodeRuntimePoint, 0)
	for rows.Next() {
		var point NodeRuntimePoint
		if err := scanRuntime(rows, &point); err != nil {
			return nil, err
		}
		out = append(out, point)
	}
	return out, rows.Err()
}
