package analytics

import (
	"context"
	"fmt"
	"strings"
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
	ctx, cancel := s.queryContext(ctx)
	defer cancel()
	rows, err := s.db.Query(ctx, `SELECT
		DISTINCT ON (site_id) site_id::text, (bytes * 8 / 3600)::bigint,
		requests::double precision / 3600
	FROM (
		SELECT site_id, bucket, sum(ingress_bytes + egress_bytes)::bigint AS bytes,
			sum(requests)::bigint AS requests
		FROM analytics.request_usage_hourly WHERE cluster_id = $1
		GROUP BY site_id, bucket
	) site_hourly
	ORDER BY site_id, bucket DESC`, cluster)
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
	EventTime           time.Time `json:"event_time"`
	SourceLogID         uint64    `json:"source_log_id"`
	RequestID           string    `json:"request_id"`
	NodeID              string    `json:"node_id"`
	ConfigVersion       uint64    `json:"config_version"`
	Hostname            string    `json:"hostname"`
	Method              string    `json:"method"`
	Scheme              string    `json:"scheme"`
	Protocol            string    `json:"protocol"`
	Path                string    `json:"path"`
	QueryString         string    `json:"query_string"`
	ClientIP            string    `json:"client_ip"`
	Country             string    `json:"country,omitempty"`
	Region              string    `json:"region,omitempty"`
	StatusCode          uint16    `json:"status_code"`
	RequestHeaderBytes  uint64    `json:"request_header_bytes"`
	RequestBodyBytes    uint64    `json:"request_body_bytes"`
	ResponseHeaderBytes uint64    `json:"response_header_bytes"`
	ResponseBodyBytes   uint64    `json:"response_body_bytes"`
	DurationUS          uint64    `json:"duration_us"`
	UpstreamAddress     string    `json:"upstream_address"`
	UpstreamStatus      uint16    `json:"upstream_status"`
	HandlerError        string    `json:"handler_error,omitempty"`
	CacheStatus         string    `json:"cache_status"`
	ContentType         string    `json:"content_type"`
	FileExtension       string    `json:"file_extension"`
	Referer             string    `json:"referer"`
	UserAgent           string    `json:"user_agent"`
	WAFAction           string    `json:"waf_action,omitempty"`
	WAFRuleID           string    `json:"waf_rule_id,omitempty"`
	WAFSource           string    `json:"waf_source,omitempty"`
	WAFMatch            string    `json:"waf_match,omitempty"`
	WAFTags             string    `json:"waf_tags,omitempty"`
}

type RequestLogPage struct {
	Items    []NodeRequestLog `json:"items"`
	Page     int              `json:"page"`
	PageSize int              `json:"page_size"`
	Total    int64            `json:"total"`
}

const requestLogColumns = `event_time, source_log_id, request_id, node_id::text, config_version,
	hostname, method, scheme, protocol, path, query_string, host(client_ip), country, region,
	status_code, request_header_bytes, request_body_bytes, response_header_bytes, response_body_bytes,
	duration_us, upstream_address, upstream_status, handler_error, cache_status, content_type, file_extension,
	referer, user_agent, waf_action, waf_rule_id, waf_source, waf_match, waf_tags`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanRequestLog(row rowScanner, item *NodeRequestLog) error {
	return row.Scan(
		&item.EventTime, &item.SourceLogID, &item.RequestID, &item.NodeID, &item.ConfigVersion,
		&item.Hostname, &item.Method, &item.Scheme, &item.Protocol, &item.Path, &item.QueryString,
		&item.ClientIP, &item.Country, &item.Region, &item.StatusCode, &item.RequestHeaderBytes,
		&item.RequestBodyBytes, &item.ResponseHeaderBytes, &item.ResponseBodyBytes, &item.DurationUS,
		&item.UpstreamAddress, &item.UpstreamStatus, &item.HandlerError, &item.CacheStatus, &item.ContentType,
		&item.FileExtension, &item.Referer, &item.UserAgent, &item.WAFAction, &item.WAFRuleID,
		&item.WAFSource, &item.WAFMatch, &item.WAFTags,
	)
}

func (s *Store) NodeRequestLogs(ctx context.Context, clusterID, nodeID string, limit int) ([]NodeRequestLog, error) {
	ctx, cancel := s.queryContext(ctx)
	defer cancel()
	rows, err := s.db.Query(ctx, `SELECT `+requestLogColumns+`
	FROM analytics.web_request_logs
	WHERE cluster_id = $1 AND node_id = $2
	ORDER BY event_time DESC, source_log_id DESC
	LIMIT $3`, clusterID, nodeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]NodeRequestLog, 0, limit)
	for rows.Next() {
		var item NodeRequestLog
		if err := scanRequestLog(rows, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) SiteRequestLogs(ctx context.Context, clusterID, siteID, search string, page, pageSize int) (RequestLogPage, error) {
	ctx, cancel := s.queryContext(ctx)
	defer cancel()
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 25
	}
	search = strings.TrimSpace(search)
	filter := `cluster_id = $1 AND site_id = $2 AND ($3 = '' OR concat_ws(' ',
		request_id, hostname, method, path, query_string, status_code::text, host(client_ip),
		cache_status, upstream_address, referer, user_agent, waf_action, waf_rule_id, waf_source
	) ILIKE '%' || $3 || '%')`
	result := RequestLogPage{Items: make([]NodeRequestLog, 0, pageSize), Page: page, PageSize: pageSize}
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM analytics.web_request_logs WHERE `+filter,
		clusterID, siteID, search).Scan(&result.Total); err != nil {
		return RequestLogPage{}, err
	}
	rows, err := s.db.Query(ctx, `SELECT `+requestLogColumns+`
	FROM analytics.web_request_logs
	WHERE `+filter+`
	ORDER BY event_time DESC, node_id, source_log_id DESC
	LIMIT $4 OFFSET $5`, clusterID, siteID, search, pageSize, (page-1)*pageSize)
	if err != nil {
		return RequestLogPage{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var item NodeRequestLog
		if err := scanRequestLog(rows, &item); err != nil {
			return RequestLogPage{}, err
		}
		result.Items = append(result.Items, item)
	}
	return result, rows.Err()
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
	ctx, cancel := s.queryContext(ctx)
	defer cancel()
	rows, err := s.db.Query(ctx, `SELECT waf_rule_id, waf_action, waf_source, waf_match,
		count(*)::bigint AS requests, count(DISTINCT client_ip)::bigint AS unique_ips, max(event_time) AS last_seen
	FROM analytics.web_request_logs
	WHERE cluster_id = $1 AND site_id = $2 AND event_time >= $3 AND event_time < $4 AND waf_action <> ''
	GROUP BY waf_rule_id, waf_action, waf_source, waf_match
	ORDER BY requests DESC, last_seen DESC
	LIMIT $5`, clusterID, siteID, from, to, limit)
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
	ctx, cancel := s.queryContext(ctx)
	defer cancel()
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	yesterday := today.AddDate(0, 0, -1)
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)

	todayTotal, err := s.usageTotal(ctx, "analytics.request_usage_hourly", cluster, site, today, now)
	if err != nil {
		return MonitoringOverview{}, err
	}
	yesterdayTotal, err := s.usageTotal(ctx, "analytics.request_usage_hourly", cluster, site, yesterday, today)
	if err != nil {
		return MonitoringOverview{}, err
	}
	monthCompleted, err := s.usageTotal(ctx, "analytics.request_usage_daily", cluster, site, month, today)
	if err != nil {
		return MonitoringOverview{}, err
	}

	monthCompleted.Requests += todayTotal.Requests
	monthCompleted.IngressBytes += todayTotal.IngressBytes
	monthCompleted.EgressBytes += todayTotal.EgressBytes
	monthCompleted.CacheEgressBytes += todayTotal.CacheEgressBytes
	filter := " WHERE cluster_id = $1"
	args := []any{cluster}
	if site != "" {
		filter += " AND site_id = $2"
		args = append(args, site)
	}
	var currentBPS, todayPeakBPS, monthPeakBPS uint64
	err = s.db.QueryRow(ctx, `WITH hourly AS (
		SELECT bucket, sum(ingress_bytes + egress_bytes)::bigint AS bytes
		FROM analytics.request_usage_hourly`+filter+` GROUP BY bucket
	) SELECT
		COALESCE((SELECT bytes / 3600 FROM hourly ORDER BY bucket DESC LIMIT 1), 0)::bigint,
		COALESCE(max(bytes / 3600) FILTER (WHERE bucket >= date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'), 0)::bigint,
		COALESCE(max(bytes / 3600) FILTER (WHERE bucket >= date_trunc('month', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'), 0)::bigint
	FROM hourly`, args...).Scan(&currentBPS, &todayPeakBPS, &monthPeakBPS)
	if err != nil {
		return MonitoringOverview{}, err
	}
	uniqueArgs := []any{cluster, today}
	uniqueFilter := "cluster_id = $1 AND day = $2::date"
	if site != "" {
		uniqueFilter += " AND site_id = $3"
		uniqueArgs = append(uniqueArgs, site)
	}
	var uniqueIPs uint64
	err = s.db.QueryRow(ctx, `SELECT count(DISTINCT client_ip)::bigint
		FROM analytics.daily_unique_ips WHERE `+uniqueFilter, uniqueArgs...).Scan(&uniqueIPs)
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
	table, cluster, site string,
	from, to time.Time,
) (UsageTotal, error) {
	q := `SELECT COALESCE(sum(requests), 0)::bigint, COALESCE(sum(ingress_bytes), 0)::bigint,
		COALESCE(sum(egress_bytes), 0)::bigint, COALESCE(sum(cache_egress_bytes), 0)::bigint
		FROM ` + table + ` WHERE cluster_id = $1 AND bucket >= $2 AND bucket < $3`
	args := []any{cluster, from, to}
	if site != "" {
		q += " AND site_id = $4"
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
	ctx, cancel := s.queryContext(ctx)
	defer cancel()
	fullStart, fullEnd := completeHourRange(from, to)
	q := `SELECT COALESCE(sum(requests), 0)::bigint,
		COALESCE(sum(ingress_bytes), 0)::bigint, COALESCE(sum(egress_bytes), 0)::bigint,
		COALESCE(sum(cache_hit_requests), 0)::bigint, COALESCE(sum(cache_miss_requests), 0)::bigint
	FROM (
		SELECT count(*)::bigint AS requests,
			COALESCE(sum(request_header_bytes + request_body_bytes), 0)::bigint AS ingress_bytes,
			COALESCE(sum(response_header_bytes + response_body_bytes), 0)::bigint AS egress_bytes,
			count(*) FILTER (WHERE upper(cache_status) = 'HIT') AS cache_hit_requests,
			count(*) FILTER (WHERE upper(cache_status) IN ('MISS', 'BYPASS')) AS cache_miss_requests
		FROM analytics.web_request_logs
		WHERE cluster_id = $1 AND event_time >= $2 AND event_time < $3`
	args := []any{cluster, from, fullStart, fullEnd, to}
	siteFilter := ""
	if site != "" {
		siteFilter = " AND site_id = $6"
		args = append(args, site)
	}
	q += siteFilter + `
		UNION ALL
		SELECT sum(requests)::bigint, sum(ingress_bytes)::bigint, sum(egress_bytes)::bigint,
			sum(cache_hit_requests)::bigint, sum(cache_miss_requests)::bigint
		FROM analytics.request_usage_hourly
		WHERE cluster_id = $1 AND bucket >= $3 AND bucket < $4` + siteFilter + `
		UNION ALL
		SELECT count(*)::bigint,
			COALESCE(sum(request_header_bytes + request_body_bytes), 0)::bigint,
			COALESCE(sum(response_header_bytes + response_body_bytes), 0)::bigint,
			count(*) FILTER (WHERE upper(cache_status) = 'HIT'),
			count(*) FILTER (WHERE upper(cache_status) IN ('MISS', 'BYPASS'))
		FROM analytics.web_request_logs
		WHERE cluster_id = $1 AND event_time >= $4 AND event_time < $5` + siteFilter + `
	) totals`

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

func completeHourRange(from, to time.Time) (time.Time, time.Time) {
	fullStart := from.Truncate(time.Hour)
	if !from.Equal(fullStart) {
		fullStart = fullStart.Add(time.Hour)
	}
	fullEnd := to.Truncate(time.Hour)
	if fullStart.After(fullEnd) {
		return to, to
	}
	return fullStart, fullEnd
}

func utcDayRange(now time.Time, days int) (time.Time, time.Time) {
	now = now.UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	return today.AddDate(0, 0, -days), today
}

func (s *Store) Top(
	ctx context.Context,
	dimension, cluster, site string,
	from, to time.Time,
	limit int,
) ([]TopItem, error) {
	ctx, cancel := s.queryContext(ctx)
	defer cancel()
	column := "path"
	if dimension == "ip" {
		column = "host(client_ip)"
	}

	q := `SELECT
		` + column + ` AS value,
		count(*)::bigint AS requests,
		sum(request_header_bytes + request_body_bytes + response_header_bytes + response_body_bytes)::bigint AS traffic
	FROM analytics.web_request_logs
	WHERE cluster_id = $1 AND event_time >= $2 AND event_time < $3`
	args := []any{cluster, from, to}

	if site != "" {
		q += " AND site_id = $4"
		args = append(args, site)
	}

	q += fmt.Sprintf(" GROUP BY value ORDER BY requests DESC LIMIT $%d", len(args)+1)
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
