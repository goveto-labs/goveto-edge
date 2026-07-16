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
	Bucket         time.Time `json:"bucket"`
	NodeID         string    `json:"node_id"`
	CPU            float32   `json:"cpu_usage_percent"`
	MemoryUsed     uint64    `json:"memory_used_bytes"`
	MemoryTotal    uint64    `json:"memory_total_bytes"`
	Load1          float32   `json:"load_1"`
	Load5          float32   `json:"load_5"`
	Load15         float32   `json:"load_15"`
	Connections    uint64    `json:"connections"`
	CacheUsed      uint64    `json:"cache_used_bytes"`
	CacheMax       uint64    `json:"cache_max_bytes"`
	CacheDirectory string    `json:"cache_directory"`
	DiskUsed       uint64    `json:"disk_used_bytes"`
	DiskTotal      uint64    `json:"disk_total_bytes"`
}

type NodeSnapshot struct {
	NodeRuntimePoint
	Online                  bool    `json:"online"`
	IngressBytesPerSecond   float64 `json:"ingress_bytes_per_second"`
	EgressBytesPerSecond    float64 `json:"egress_bytes_per_second"`
	RequestsPerMinute       uint64  `json:"requests_per_minute"`
	DiskWriteBytesPerSecond float64 `json:"disk_write_bytes_per_second"`
}

func (s *Store) LatestNodeRuntime(ctx context.Context, cluster, nodeID string) ([]NodeSnapshot, error) {
	q := `SELECT
		minute,
		toString(node_id),
		cpu_usage_percent,
		memory_used_bytes,
		memory_total_bytes,
		load_1,
		load_5,
		load_15,
		connections,
		cache_used_bytes,
		cache_max_bytes,
		cache_directory,
		disk_used_bytes,
		disk_total_bytes
	FROM goveto.node_runtime_metrics_minute
	WHERE cluster_id = ?`
	args := []any{cluster}
	if nodeID != "" {
		q += " AND node_id = ?"
		args = append(args, nodeID)
	}
	q += " ORDER BY node_id, minute DESC LIMIT 2 BY node_id"
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byNode := map[string][]NodeRuntimePoint{}
	order := []string{}
	for rows.Next() {
		var x NodeRuntimePoint
		if err = rows.Scan(
			&x.Bucket,
			&x.NodeID,
			&x.CPU,
			&x.MemoryUsed,
			&x.MemoryTotal,
			&x.Load1,
			&x.Load5,
			&x.Load15,
			&x.Connections,
			&x.CacheUsed,
			&x.CacheMax,
			&x.CacheDirectory,
			&x.DiskUsed,
			&x.DiskTotal,
		); err != nil {
			return nil, err
		}
		if _, ok := byNode[x.NodeID]; !ok {
			order = append(order, x.NodeID)
		}
		byNode[x.NodeID] = append(byNode[x.NodeID], x)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	traffic, err := s.latestNodeTraffic(ctx, cluster, nodeID)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	out := make([]NodeSnapshot, 0, len(order))
	for _, nodeID := range order {
		points := byNode[nodeID]
		snapshot := NodeSnapshot{
			NodeRuntimePoint: points[0],
			Online:           now.Sub(points[0].Bucket) <= 3*time.Minute,
		}
		if len(points) > 1 && points[0].CacheUsed > points[1].CacheUsed {
			seconds := points[0].Bucket.Sub(points[1].Bucket).Seconds()
			if seconds > 0 {
				snapshot.DiskWriteBytesPerSecond = float64(points[0].CacheUsed-points[1].CacheUsed) / seconds
			}
		}
		if current, ok := traffic[nodeID]; ok {
			snapshot.IngressBytesPerSecond = float64(current.IngressBytes) / 60
			snapshot.EgressBytesPerSecond = float64(current.EgressBytes) / 60
			snapshot.RequestsPerMinute = current.Requests
		}
		out = append(out, snapshot)
	}

	return out, nil
}

type nodeTrafficMinute struct {
	Requests     uint64
	IngressBytes uint64
	EgressBytes  uint64
}

func (s *Store) latestNodeTraffic(ctx context.Context, cluster, nodeID string) (map[string]nodeTrafficMinute, error) {
	q := `SELECT
		toString(node_id),
		sum(requests),
		sum(ingress_bytes),
		sum(egress_bytes)
	FROM goveto.node_traffic_metrics_minute
	WHERE cluster_id = ? AND minute >= now() - INTERVAL 1 MINUTE`
	args := []any{cluster}
	if nodeID != "" {
		q += " AND node_id = ?"
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
		var nodeID string
		var current nodeTrafficMinute
		if err := rows.Scan(&nodeID, &current.Requests, &current.IngressBytes, &current.EgressBytes); err != nil {
			return nil, err
		}
		out[nodeID] = current
	}
	return out, rows.Err()
}

func (s *Store) TrafficSeries(ctx context.Context, cluster, site, nodeID, period string) ([]TrafficPoint, error) {
	from := time.Now().UTC().Add(-24 * time.Hour)
	q := `SELECT bucket, sum(requests), sum(ingress_bytes), sum(egress_bytes), sum(cache_egress_bytes)
		FROM goveto.request_usage_hourly
		WHERE cluster_id = ? AND bucket >= ?`
	args := []any{cluster, from}
	if site != "" {
		q += " AND site_id = ?"
		args = append(args, site)
	}
	if nodeID != "" {
		q += " AND node_id = ?"
		args = append(args, nodeID)
	}
	q += " GROUP BY bucket ORDER BY bucket"

	if period == "30d" {
		from = time.Now().UTC().AddDate(0, 0, -30)
		today := time.Now().UTC().Truncate(24 * time.Hour)
		siteDaily, siteHourly := "", ""
		nodeDaily, nodeHourly := "", ""
		args = []any{cluster, from, today}
		if site != "" {
			siteDaily = " AND site_id = ?"
			args = append(args, site)
		}
		if nodeID != "" {
			nodeDaily = " AND node_id = ?"
			args = append(args, nodeID)
		}
		args = append(args, cluster, today)
		if site != "" {
			siteHourly = " AND site_id = ?"
			args = append(args, site)
		}
		if nodeID != "" {
			nodeHourly = " AND node_id = ?"
			args = append(args, nodeID)
		}
		q = fmt.Sprintf(`SELECT bucket,
			sum(requests), sum(ingress_bytes), sum(egress_bytes), sum(cache_egress_bytes)
		FROM (
			SELECT toDateTime(bucket, 'UTC') AS bucket, requests, ingress_bytes, egress_bytes, cache_egress_bytes
			FROM goveto.request_usage_daily
			WHERE cluster_id = ? AND bucket >= toDate(?) AND bucket < toDate(?)%s%s
			UNION ALL
			SELECT toStartOfDay(bucket) AS bucket, requests, ingress_bytes, egress_bytes, cache_egress_bytes
			FROM goveto.request_usage_hourly
			WHERE cluster_id = ? AND bucket >= ?%s%s
		)
		GROUP BY bucket ORDER BY bucket`, siteDaily, nodeDaily, siteHourly, nodeHourly)
	}

	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []TrafficPoint{}
	for rows.Next() {
		var x TrafficPoint
		if err = rows.Scan(
			&x.Bucket,
			&x.Requests,
			&x.IngressBytes,
			&x.EgressBytes,
			&x.CacheEgressBytes,
		); err != nil {
			return nil, err
		}
		out = append(out, x)
	}

	return out, rows.Err()
}

var dimensions = map[string]string{
	"extension":      "file_extension",
	"file_extension": "file_extension",
	"hostname":       "hostname",
	"domain":         "hostname",
	"referer":        "referer",
	"status":         "status_code",
	"method":         "method",
	"path":           "path",
	"ip":             "client_ip",
	"node":           "node_id",
}

func (s *Store) Ranking(
	ctx context.Context,
	cluster, site, period, dimension, sortBy string,
	limit int,
) ([]DistributionItem, error) {
	if dimension == "domain" {
		dimension = "hostname"
	}
	if dimension == "file_extension" {
		dimension = "extension"
	}

	col, ok := dimensions[dimension]
	if !ok {
		return nil, ErrInvalidDimension
	}
	if period == "30d" {
		return s.ranking30d(ctx, cluster, site, dimension, sortBy, limit)
	}

	from := time.Now().UTC().Add(-24 * time.Hour)
	table := "goveto.web_request_logs"
	timeCol := "event_time"
	value := "toString(" + col + ")"
	requests := "count()"
	ingress := "sum(request_header_bytes+request_body_bytes)"
	egress := "sum(response_header_bytes+response_body_bytes)"

	q := fmt.Sprintf(
		`SELECT %s, %s, %s, %s
		 FROM %s
		 WHERE cluster_id = ? AND %s >= ?`,
		value, requests, ingress, egress, table, timeCol,
	)
	args := []any{cluster, from}

	if site != "" {
		q += " AND site_id = ?"
		args = append(args, site)
	}

	q += " GROUP BY 1 ORDER BY "
	if sortBy == "traffic" {
		q += "(" + ingress + ")+ (" + egress + ") DESC"
	} else {
		q += requests + " DESC"
	}
	q += " LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []DistributionItem{}
	for rows.Next() {
		var x DistributionItem
		if err = rows.Scan(&x.Value, &x.Requests, &x.IngressBytes, &x.EgressBytes); err != nil {
			return nil, err
		}
		out = append(out, x)
	}

	return out, rows.Err()
}

func (s *Store) ranking30d(
	ctx context.Context,
	cluster, site, dimension, sortBy string,
	limit int,
) ([]DistributionItem, error) {
	now := time.Now().UTC()
	from := now.AddDate(0, 0, -30)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	dailyTable, currentTable := "goveto.request_high_cardinality_daily", ""
	dailyValue, currentValue := "value", "value"
	dimensionValue := ""

	switch dimension {
	case "node":
		dailyTable = "goveto.request_usage_daily"
		currentTable = "goveto.request_usage_hourly"
		dailyValue, currentValue = "toString(node_id)", "toString(node_id)"
	case "extension", "status", "method":
		dailyTable = "goveto.request_breakdown_daily"
		currentTable = "goveto.request_breakdown_hourly"
		dimensionValue = map[string]string{
			"extension": "file_extension",
			"status":    "status_code",
			"method":    "method",
		}[dimension]
	case "hostname":
		currentTable = "goveto.request_hostname_hourly"
		currentValue = "hostname"
		dimensionValue = "hostname"
	case "referer":
		currentTable = "goveto.request_referer_hourly"
		currentValue = "referer"
		dimensionValue = "referer"
	case "path":
		currentTable = "goveto.request_path_hourly"
		currentValue = "path"
		dimensionValue = "path"
	case "ip":
		currentTable = "goveto.request_ip_hourly"
		currentValue = "toString(client_ip)"
		dimensionValue = "client_ip"
	default:
		return nil, ErrInvalidDimension
	}

	dailyFilters, currentFilters := "", ""
	args := []any{cluster, from, today}
	if site != "" {
		dailyFilters += " AND site_id = ?"
		args = append(args, site)
	}
	if dimensionValue != "" && dailyTable != "goveto.request_usage_daily" {
		dailyFilters += " AND dimension = ?"
		args = append(args, dimensionValue)
	}
	args = append(args, cluster, today)
	if site != "" {
		currentFilters += " AND site_id = ?"
		args = append(args, site)
	}
	if dimensionValue != "" && currentTable == "goveto.request_breakdown_hourly" {
		currentFilters += " AND dimension = ?"
		args = append(args, dimensionValue)
	}

	q := fmt.Sprintf(`SELECT value, sum(requests), sum(ingress_bytes), sum(egress_bytes)
		FROM (
			SELECT %s AS value, requests, ingress_bytes, egress_bytes
			FROM %s
			WHERE cluster_id = ? AND bucket >= ? AND bucket < ?%s
			UNION ALL
			SELECT %s AS value, requests, ingress_bytes, egress_bytes
			FROM %s
			WHERE cluster_id = ? AND bucket >= ?%s
		)
		GROUP BY value ORDER BY `,
		dailyValue, dailyTable, dailyFilters,
		currentValue, currentTable, currentFilters,
	)
	if sortBy == "traffic" {
		q += "(sum(ingress_bytes) + sum(egress_bytes)) DESC"
	} else {
		q += "sum(requests) DESC"
	}
	q += " LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DistributionItem{}
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
	interval := "toStartOfFiveMinutes(minute)"
	from := time.Now().UTC().Add(-12 * time.Hour)

	if period == "24h" {
		from = time.Now().UTC().Add(-24 * time.Hour)
	} else if period == "30d" {
		interval = "toStartOfHour(minute)"
		from = time.Now().UTC().AddDate(0, 0, -30)
	}

	q := `SELECT ` + interval + `,
		toString(node_id),
		toFloat32(avg(cpu_usage_percent)),
		toUInt64(avg(memory_used_bytes)),
		toUInt64(avg(memory_total_bytes)),
		toFloat32(avg(load_1)),
		toFloat32(avg(load_5)),
		toFloat32(avg(load_15)),
		toUInt64(avg(connections)),
		toUInt64(avg(cache_used_bytes)),
		toUInt64(avg(cache_max_bytes)),
		argMax(cache_directory, minute),
		toUInt64(avg(disk_used_bytes)),
		toUInt64(avg(disk_total_bytes))
	FROM goveto.node_runtime_metrics_minute
	WHERE cluster_id = ? AND minute >= ?`
	args := []any{cluster, from}

	if nodeID != "" {
		q += " AND node_id = ?"
		args = append(args, nodeID)
	}

	q += " GROUP BY 1, 2 ORDER BY 1, 2"

	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []NodeRuntimePoint{}
	for rows.Next() {
		var x NodeRuntimePoint
		if err = rows.Scan(
			&x.Bucket,
			&x.NodeID,
			&x.CPU,
			&x.MemoryUsed,
			&x.MemoryTotal,
			&x.Load1,
			&x.Load5,
			&x.Load15,
			&x.Connections,
			&x.CacheUsed,
			&x.CacheMax,
			&x.CacheDirectory,
			&x.DiskUsed,
			&x.DiskTotal,
		); err != nil {
			return nil, err
		}
		out = append(out, x)
	}

	return out, rows.Err()
}
