-- Run once per completed UTC day. Replace {date} with YYYY-MM-DD.
-- Delete-before-insert makes retries idempotent.

ALTER TABLE goveto.request_usage_daily DELETE WHERE bucket = toDate('{date}') SETTINGS mutations_sync = 2;

INSERT INTO goveto.request_usage_daily
SELECT
    toDate(hourly.bucket) AS bucket,
    hourly.cluster_id,
    hourly.node_id,
    hourly.site_id,
    sum(hourly.requests),
    sum(hourly.ingress_bytes),
    sum(hourly.egress_bytes),
    sum(hourly.cache_hit_requests),
    sum(hourly.cache_miss_requests),
    sum(hourly.cache_egress_bytes),
    sum(hourly.origin_requests),
    sum(hourly.error_requests),
    sum(hourly.duration_us_sum),
    sum(hourly.duration_count),
    uniqCombined64MergeState(hourly.unique_ip_state)
FROM goveto.request_usage_hourly AS hourly
WHERE hourly.bucket >= toDateTime('{date}', 'UTC')
  AND hourly.bucket < toDateTime('{date}', 'UTC') + INTERVAL 1 DAY
GROUP BY toDate(hourly.bucket), hourly.cluster_id, hourly.node_id, hourly.site_id;

ALTER TABLE goveto.request_breakdown_daily DELETE WHERE toDate(bucket) = toDate('{date}') SETTINGS mutations_sync = 2;

INSERT INTO goveto.request_breakdown_daily
SELECT
    toStartOfDay(hourly.bucket) AS bucket,
    hourly.cluster_id,
    hourly.site_id,
    hourly.dimension,
    hourly.value,
    sum(hourly.requests),
    sum(hourly.ingress_bytes),
    sum(hourly.egress_bytes)
FROM goveto.request_breakdown_hourly AS hourly
WHERE hourly.bucket >= toDateTime('{date}', 'UTC')
  AND hourly.bucket < toDateTime('{date}', 'UTC') + INTERVAL 1 DAY
GROUP BY toStartOfDay(hourly.bucket), hourly.cluster_id, hourly.site_id, hourly.dimension, hourly.value;

ALTER TABLE goveto.request_high_cardinality_daily DELETE WHERE bucket = toDate('{date}') SETTINGS mutations_sync = 2;

INSERT INTO goveto.request_high_cardinality_daily
SELECT toDate(source.bucket), source.cluster_id, source.site_id, source.dimension, source.value,
       sum(source.requests), sum(source.ingress_bytes), sum(source.egress_bytes)
FROM
(
    SELECT hourly.bucket, hourly.cluster_id, hourly.site_id, 'path' AS dimension, hourly.path AS value,
           hourly.requests, hourly.ingress_bytes, hourly.egress_bytes
    FROM goveto.request_path_hourly AS hourly
    WHERE hourly.bucket >= toDateTime('{date}', 'UTC') AND hourly.bucket < toDateTime('{date}', 'UTC') + INTERVAL 1 DAY
    UNION ALL
    SELECT hourly.bucket, hourly.cluster_id, hourly.site_id, 'client_ip', toString(hourly.client_ip),
           hourly.requests, hourly.ingress_bytes, hourly.egress_bytes
    FROM goveto.request_ip_hourly AS hourly
    WHERE hourly.bucket >= toDateTime('{date}', 'UTC') AND hourly.bucket < toDateTime('{date}', 'UTC') + INTERVAL 1 DAY
    UNION ALL
    SELECT hourly.bucket, hourly.cluster_id, hourly.site_id, 'hostname', hourly.hostname,
           hourly.requests, hourly.ingress_bytes, hourly.egress_bytes
    FROM goveto.request_hostname_hourly AS hourly
    WHERE hourly.bucket >= toDateTime('{date}', 'UTC') AND hourly.bucket < toDateTime('{date}', 'UTC') + INTERVAL 1 DAY
    UNION ALL
    SELECT hourly.bucket, hourly.cluster_id, hourly.site_id, 'referer', hourly.referer,
           hourly.requests, hourly.ingress_bytes, hourly.egress_bytes
    FROM goveto.request_referer_hourly AS hourly
    WHERE hourly.bucket >= toDateTime('{date}', 'UTC') AND hourly.bucket < toDateTime('{date}', 'UTC') + INTERVAL 1 DAY
) AS source
GROUP BY toDate(source.bucket), source.cluster_id, source.site_id, source.dimension, source.value;
