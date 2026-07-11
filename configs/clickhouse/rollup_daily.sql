-- Run once per completed UTC day. Replace {date} with YYYY-MM-DD.
-- Delete-before-insert makes retries idempotent.

ALTER TABLE goveto.request_usage_daily DELETE WHERE bucket = toDate('{date}') SETTINGS mutations_sync = 2;

INSERT INTO goveto.request_usage_daily
SELECT
    toDate(bucket) AS bucket,
    cluster_id,
    node_id,
    site_id,
    sum(requests),
    sum(ingress_bytes),
    sum(egress_bytes),
    sum(cache_hit_requests),
    sum(cache_miss_requests),
    sum(cache_egress_bytes),
    sum(origin_requests),
    sum(error_requests),
    sum(duration_us_sum),
    sum(duration_count),
    uniqCombined64MergeState(unique_ip_state)
FROM goveto.request_usage_hourly
WHERE bucket >= toDateTime('{date}', 'UTC')
  AND bucket < toDateTime('{date}', 'UTC') + INTERVAL 1 DAY
GROUP BY toDate(bucket), cluster_id, node_id, site_id;

ALTER TABLE goveto.request_breakdown_daily DELETE WHERE toDate(bucket) = toDate('{date}') SETTINGS mutations_sync = 2;

INSERT INTO goveto.request_breakdown_daily
SELECT
    toStartOfDay(bucket) AS bucket,
    cluster_id,
    site_id,
    dimension,
    value,
    sum(requests),
    sum(ingress_bytes),
    sum(egress_bytes)
FROM goveto.request_breakdown_hourly
WHERE bucket >= toDateTime('{date}', 'UTC')
  AND bucket < toDateTime('{date}', 'UTC') + INTERVAL 1 DAY
GROUP BY toStartOfDay(bucket), cluster_id, site_id, dimension, value;

ALTER TABLE goveto.request_high_cardinality_daily DELETE WHERE bucket = toDate('{date}') SETTINGS mutations_sync = 2;

INSERT INTO goveto.request_high_cardinality_daily
SELECT toDate(bucket), cluster_id, site_id, dimension, value,
       sum(requests), sum(ingress_bytes), sum(egress_bytes)
FROM
(
    SELECT bucket, cluster_id, site_id, 'path' AS dimension, path AS value,
           requests, ingress_bytes, egress_bytes
    FROM goveto.request_path_hourly
    WHERE bucket >= toDateTime('{date}', 'UTC') AND bucket < toDateTime('{date}', 'UTC') + INTERVAL 1 DAY
    UNION ALL
    SELECT bucket, cluster_id, site_id, 'client_ip', toString(client_ip),
           requests, ingress_bytes, egress_bytes
    FROM goveto.request_ip_hourly
    WHERE bucket >= toDateTime('{date}', 'UTC') AND bucket < toDateTime('{date}', 'UTC') + INTERVAL 1 DAY
    UNION ALL
    SELECT bucket, cluster_id, site_id, 'hostname', hostname,
           requests, ingress_bytes, egress_bytes
    FROM goveto.request_hostname_hourly
    WHERE bucket >= toDateTime('{date}', 'UTC') AND bucket < toDateTime('{date}', 'UTC') + INTERVAL 1 DAY
    UNION ALL
    SELECT bucket, cluster_id, site_id, 'referer', referer,
           requests, ingress_bytes, egress_bytes
    FROM goveto.request_referer_hourly
    WHERE bucket >= toDateTime('{date}', 'UTC') AND bucket < toDateTime('{date}', 'UTC') + INTERVAL 1 DAY
)
GROUP BY toDate(bucket), cluster_id, site_id, dimension, value;
