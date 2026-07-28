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
	Today                 UsageTotal `json:"today"`
	Yesterday             UsageTotal `json:"yesterday"`
	Month                 UsageTotal `json:"month"`
	CurrentBandwidthBPS   uint64     `json:"current_bandwidth_bps"`
	TodayPeakBandwidthBPS uint64     `json:"today_peak_bandwidth_bps"`
	MonthPeakBandwidthBPS uint64     `json:"month_peak_bandwidth_bps"`
	TodayUniqueIPs        uint64     `json:"today_unique_ips"`
}

type SiteRate struct {
	SiteID       string  `json:"site_id"`
	BandwidthBPS uint64  `json:"bandwidth_bps"`
	QPS          float64 `json:"qps"`
}

func (s *Store) LatestSiteRates(ctx context.Context, cluster string) ([]SiteRate, error) {
	rows, err := s.db.Query(ctx, `SELECT
		site_id,
		toUInt64(argMax((ingress_bytes + egress_bytes) * 8 / 3600, bucket)),
		toFloat64(argMax(requests / 3600, bucket))
	FROM goveto.request_usage_hourly
	WHERE cluster_id = ? AND site_id != ''
	GROUP BY site_id`, cluster)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]SiteRate, 0)
	for rows.Next() {
		var item SiteRate
		if err := rows.Scan(&item.SiteID, &item.BandwidthBPS, &item.QPS); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

type NodeRequestLog struct {
	EventTime       time.Time `json:"event_time"`
	RequestID       string    `json:"request_id"`
	ConfigVersion   uint64    `json:"config_version"`
	Hostname        string    `json:"hostname"`
	Method          string    `json:"method"`
	Path            string    `json:"path"`
	StatusCode      uint16    `json:"status_code"`
	DurationUS      uint64    `json:"duration_us"`
	UpstreamAddress string    `json:"upstream_address"`
	CacheStatus     string    `json:"cache_status"`
	WAFAction       string    `json:"waf_action,omitempty"`
	WAFRuleID       string    `json:"waf_rule_id,omitempty"`
	WAFSource       string    `json:"waf_source,omitempty"`
	WAFMatch        string    `json:"waf_match,omitempty"`
	WAFTags         string    `json:"waf_tags,omitempty"`
}

func (s *Store) NodeRequestLogs(ctx context.Context, clusterID, nodeID string, limit int) ([]NodeRequestLog, error) {
	rows, err := s.db.Query(ctx, `SELECT
		event_time, request_id, config_version, hostname, method, path, status_code,
		duration_us, upstream_address, cache_status,
		waf_action, waf_rule_id, waf_source, waf_match, waf_tags
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
		if err := rows.Scan(&item.EventTime, &item.RequestID, &item.ConfigVersion, &item.Hostname, &item.Method, &item.Path, &item.StatusCode, &item.DurationUS, &item.UpstreamAddress, &item.CacheStatus, &item.WAFAction, &item.WAFRuleID, &item.WAFSource, &item.WAFMatch, &item.WAFTags); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) SiteRequestLogs(ctx context.Context, clusterID, siteID string, limit int) ([]NodeRequestLog, error) {
	rows, err := s.db.Query(ctx, `SELECT
		event_time, request_id, config_version, hostname, method, path, status_code,
		duration_us, upstream_address, cache_status,
		waf_action, waf_rule_id, waf_source, waf_match, waf_tags
	FROM goveto.web_request_logs
	WHERE cluster_id = ? AND site_id = ?
	ORDER BY event_time DESC
	LIMIT ?`, clusterID, siteID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]NodeRequestLog, 0, limit)
	for rows.Next() {
		var item NodeRequestLog
		if err := rows.Scan(&item.EventTime, &item.RequestID, &item.ConfigVersion, &item.Hostname, &item.Method, &item.Path, &item.StatusCode, &item.DurationUS, &item.UpstreamAddress, &item.CacheStatus, &item.WAFAction, &item.WAFRuleID, &item.WAFSource, &item.WAFMatch, &item.WAFTags); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

type WAFRuleStat struct {
	RuleID    string    `json:"rule_id"`
	Action    string    `json:"action"`
	Source    string    `json:"source"`
	Match     string    `json:"match"`
	Requests  uint64    `json:"requests"`
	UniqueIPs uint64    `json:"unique_ips"`
	LastSeen  time.Time `json:"last_seen"`
}

func (s *Store) WAFRuleStats(ctx context.Context, clusterID, siteID string, from, to time.Time, limit int) ([]WAFRuleStat, error) {
	rows, err := s.db.Query(ctx, `SELECT waf_rule_id, waf_action, waf_source, waf_match,
		count() AS requests, uniqExact(client_ip) AS unique_ips, max(event_time) AS last_seen
	FROM goveto.web_request_logs
	WHERE cluster_id = ? AND site_id = ? AND event_time >= ? AND event_time < ? AND waf_action != ''
	GROUP BY waf_rule_id, waf_action, waf_source, waf_match
	ORDER BY requests DESC, last_seen DESC
	LIMIT ?`, clusterID, siteID, from, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]WAFRuleStat, 0, limit)
	for rows.Next() {
		var item WAFRuleStat
		if err := rows.Scan(&item.RuleID, &item.Action, &item.Source, &item.Match, &item.Requests, &item.UniqueIPs, &item.LastSeen); err != nil {
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
	filter := " WHERE cluster_id = ?"
	args := []any{cluster}
	if site != "" {
		filter += " AND site_id = ?"
		args = append(args, site)
	}
	var currentBPS, todayPeakBPS, monthPeakBPS uint64
	err = s.db.QueryRow(ctx, `SELECT
		toUInt64(ifNull(argMax((ingress_bytes + egress_bytes) / 3600, bucket), 0)),
		toUInt64(ifNull(maxIf((ingress_bytes + egress_bytes) / 3600, bucket >= toStartOfDay(now('UTC'))), 0)),
		toUInt64(ifNull(maxIf((ingress_bytes + egress_bytes) / 3600, bucket >= toStartOfMonth(now('UTC'))), 0))
	FROM goveto.request_usage_hourly`+filter, args...).Scan(&currentBPS, &todayPeakBPS, &monthPeakBPS)
	if err != nil {
		return MonitoringOverview{}, err
	}
	uniqueArgs := append(append([]any{}, args...), today)
	var uniqueIPs uint64
	err = s.db.QueryRow(ctx, `SELECT uniqExact(client_ip) FROM goveto.web_request_logs`+filter+` AND event_time >= ?`, uniqueArgs...).Scan(&uniqueIPs)
	if err != nil {
		return MonitoringOverview{}, err
	}
	return MonitoringOverview{
		Today: todayTotal, Yesterday: yesterdayTotal, Month: monthCompleted,
		CurrentBandwidthBPS: currentBPS, TodayPeakBandwidthBPS: todayPeakBPS,
		MonthPeakBandwidthBPS: monthPeakBPS, TodayUniqueIPs: uniqueIPs,
	}, nil
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
