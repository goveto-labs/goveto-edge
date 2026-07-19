package analytics

import (
	"context"
	"time"
)

type Summary struct {
	Requests     uint64  `json:"requests"`
	IngressBytes uint64  `json:"ingress_bytes"`
	EgressBytes  uint64  `json:"egress_bytes"`
	CacheHits    uint64  `json:"cache_hits"`
	CacheMisses  uint64  `json:"cache_misses"`
	HitRate      float64 `json:"hit_rate"`
}

type TopItem struct {
	Value        string `json:"value"`
	Requests     uint64 `json:"requests"`
	TrafficBytes uint64 `json:"traffic_bytes"`
}

type UsageTotal struct {
	Requests         uint64 `json:"requests"`
	IngressBytes     uint64 `json:"ingress_bytes"`
	EgressBytes      uint64 `json:"egress_bytes"`
	CacheEgressBytes uint64 `json:"cache_egress_bytes"`
}

type MonitoringOverview struct {
	Today     UsageTotal `json:"today"`
	Yesterday UsageTotal `json:"yesterday"`
	Month     UsageTotal `json:"month"`
}

type NodeRequestLog struct {
	EventTime       time.Time `json:"event_time"`
	RequestID       string    `json:"request_id"`
	Hostname        string    `json:"hostname"`
	Method          string    `json:"method"`
	Path            string    `json:"path"`
	StatusCode      uint16    `json:"status_code"`
	DurationUS      uint64    `json:"duration_us"`
	UpstreamAddress string    `json:"upstream_address"`
	CacheStatus     string    `json:"cache_status"`
}

func (s *Store) NodeRequestLogs(ctx context.Context, clusterID, nodeID string, limit int) ([]NodeRequestLog, error) {
	rows, err := s.db.Query(ctx, `SELECT
		event_time, request_id, hostname, method, path, status_code,
		duration_us, upstream_address, cache_status
	FROM goveto.web_request_logs
	WHERE cluster_id = ? AND node_id = ?
	ORDER BY event_time DESC
	LIMIT ?`, clusterID, nodeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]NodeRequestLog, 0, limit)
	for rows.Next() {
		var item NodeRequestLog
		if err := rows.Scan(&item.EventTime, &item.RequestID, &item.Hostname, &item.Method, &item.Path, &item.StatusCode, &item.DurationUS, &item.UpstreamAddress, &item.CacheStatus); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) MonitoringOverview(ctx context.Context, cluster, site string) (MonitoringOverview, error) {
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	yesterday := today.AddDate(0, 0, -1)
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)

	todayTotal, err := s.usageTotal(ctx, "goveto.request_usage_hourly", "bucket", cluster, site, today, now)
	if err != nil {
		return MonitoringOverview{}, err
	}
	yesterdayTotal, err := s.usageTotal(ctx, "goveto.request_usage_hourly", "bucket", cluster, site, yesterday, today)
	if err != nil {
		return MonitoringOverview{}, err
	}
	monthCompleted, err := s.usageTotal(ctx, "goveto.request_usage_daily", "bucket", cluster, site, month, today)
	if err != nil {
		return MonitoringOverview{}, err
	}

	monthCompleted.Requests += todayTotal.Requests
	monthCompleted.IngressBytes += todayTotal.IngressBytes
	monthCompleted.EgressBytes += todayTotal.EgressBytes
	monthCompleted.CacheEgressBytes += todayTotal.CacheEgressBytes
	return MonitoringOverview{Today: todayTotal, Yesterday: yesterdayTotal, Month: monthCompleted}, nil
}

func (s *Store) usageTotal(
	ctx context.Context,
	table, timeColumn, cluster, site string,
	from, to time.Time,
) (UsageTotal, error) {
	q := `SELECT sum(requests), sum(ingress_bytes), sum(egress_bytes), sum(cache_egress_bytes)
		FROM ` + table + ` WHERE cluster_id = ? AND ` + timeColumn + ` >= ? AND ` + timeColumn + ` < ?`
	args := []any{cluster, from, to}
	if site != "" {
		q += " AND site_id = ?"
		args = append(args, site)
	}
	var total UsageTotal
	err := s.db.QueryRow(ctx, q, args...).Scan(
		&total.Requests,
		&total.IngressBytes,
		&total.EgressBytes,
		&total.CacheEgressBytes,
	)
	return total, err
}

func (s *Store) Summary(ctx context.Context, cluster, site string, from, to time.Time) (Summary, error) {
	q := `SELECT
		count(),
		sum(request_header_bytes + request_body_bytes),
		sum(response_header_bytes + response_body_bytes),
		countIf(upper(cache_status) = 'HIT'),
		countIf(upper(cache_status) IN ('MISS', 'BYPASS'))
	FROM goveto.web_request_logs
	WHERE cluster_id = ? AND event_time >= ? AND event_time < ?`
	args := []any{cluster, from, to}

	if site != "" {
		q += " AND site_id = ?"
		args = append(args, site)
	}

	var v Summary
	err := s.db.QueryRow(ctx, q, args...).Scan(
		&v.Requests,
		&v.IngressBytes,
		&v.EgressBytes,
		&v.CacheHits,
		&v.CacheMisses,
	)
	if v.CacheHits+v.CacheMisses > 0 {
		v.HitRate = float64(v.CacheHits) / float64(v.CacheHits+v.CacheMisses)
	}

	return v, err
}

func (s *Store) Top(
	ctx context.Context,
	dimension, cluster, site string,
	from, to time.Time,
	limit int,
) ([]TopItem, error) {
	column := "path"
	if dimension == "ip" {
		column = "toString(client_ip)"
	}

	q := `SELECT
		` + column + ` AS value,
		count() AS requests,
		sum(request_header_bytes + request_body_bytes + response_header_bytes + response_body_bytes) AS traffic
	FROM goveto.web_request_logs
	WHERE cluster_id = ? AND event_time >= ? AND event_time < ?`
	args := []any{cluster, from, to}

	if site != "" {
		q += " AND site_id = ?"
		args = append(args, site)
	}

	q += " GROUP BY value ORDER BY requests DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []TopItem{}
	for rows.Next() {
		var x TopItem
		if err = rows.Scan(&x.Value, &x.Requests, &x.TrafficBytes); err != nil {
			return nil, err
		}
		out = append(out, x)
	}

	return out, rows.Err()
}
