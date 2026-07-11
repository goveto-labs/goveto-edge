package analytics

import (
	"context"
	"fmt"
	"time"
)

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
	Bucket      time.Time `json:"bucket"`
	NodeID      string    `json:"node_id"`
	CPU         float32   `json:"cpu_usage_percent"`
	MemoryUsed  uint64    `json:"memory_used_bytes"`
	MemoryTotal uint64    `json:"memory_total_bytes"`
	Load1       float32   `json:"load_1"`
	Load5       float32   `json:"load_5"`
	Load15      float32   `json:"load_15"`
}

func (s *Store) TrafficSeries(ctx context.Context, cluster, site, period string) ([]TrafficPoint, error) {
	daily := period == "30d"
	table := "goveto.request_usage_hourly"
	bucket := "bucket"
	from := time.Now().UTC().Add(-24 * time.Hour)

	if daily {
		table = "goveto.request_usage_daily"
		bucket = "toDateTime(bucket, 'UTC')"
		from = time.Now().UTC().AddDate(0, 0, -30)
	}

	q := fmt.Sprintf(
		`SELECT %s, sum(requests), sum(ingress_bytes), sum(egress_bytes), sum(cache_egress_bytes)
		 FROM %s
		 WHERE cluster_id = ? AND bucket >= ?`,
		bucket, table,
	)
	args := []any{cluster, from}

	if site != "" {
		q += " AND site_id = ?"
		args = append(args, site)
	}

	q += " GROUP BY 1 ORDER BY 1"

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
		return nil, fmt.Errorf("invalid dimension")
	}

	daily := period == "30d"
	from := time.Now().UTC().Add(-24 * time.Hour)
	table := "goveto.web_request_logs"
	timeCol := "event_time"
	value := "toString(" + col + ")"
	requests := "count()"
	ingress := "sum(request_header_bytes+request_body_bytes)"
	egress := "sum(response_header_bytes+response_body_bytes)"

	if daily {
		from = time.Now().UTC().AddDate(0, 0, -30)
		timeCol = "bucket"

		if dimension == "node" {
			table = "goveto.request_usage_daily"
			value = "toString(node_id)"
		} else if dimension == "extension" || dimension == "status" || dimension == "method" {
			table = "goveto.request_breakdown_daily"
			value = "value"
		} else {
			table = "goveto.request_high_cardinality_daily"
			value = "value"
		}

		requests = "sum(requests)"
		ingress = "sum(ingress_bytes)"
		egress = "sum(egress_bytes)"
	}

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

	if daily && table == "goveto.request_breakdown_daily" {
		q += " AND dimension = ?"
		args = append(args, map[string]string{
			"extension": "file_extension",
			"status":    "status_code",
			"method":    "method",
		}[dimension])
	}

	if daily && table == "goveto.request_high_cardinality_daily" {
		q += " AND dimension = ?"
		args = append(args, map[string]string{
			"hostname": "hostname",
			"referer":  "referer",
			"path":     "path",
			"ip":       "client_ip",
		}[dimension])
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

func (s *Store) NodeRuntime(ctx context.Context, cluster, nodeID, period string) ([]NodeRuntimePoint, error) {
	interval := "toStartOfFiveMinutes(minute)"
	from := time.Now().UTC().Add(-24 * time.Hour)

	if period == "30d" {
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
		toFloat32(avg(load_15))
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
		); err != nil {
			return nil, err
		}
		out = append(out, x)
	}

	return out, rows.Err()
}
