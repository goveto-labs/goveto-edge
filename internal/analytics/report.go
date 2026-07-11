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

func (s *Store) Summary(ctx context.Context, cluster, site string, from, to time.Time) (Summary, error) {
	q := `SELECT
		count(),
		sum(request_header_bytes + request_body_bytes),
		sum(response_header_bytes + response_body_bytes),
		countIf(cache_status = 'HIT'),
		countIf(cache_status IN ('MISS', 'BYPASS'))
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
