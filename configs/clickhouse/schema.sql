CREATE DATABASE IF NOT EXISTS goveto;

-- The only first-hand source for HTTP analytics. Body content is never stored;
-- only header/body byte counts are recorded for traffic accounting.
CREATE TABLE IF NOT EXISTS goveto.web_request_logs
(
    event_time DateTime64(3, 'UTC'),
    request_id String,
    cluster_id UUID,
    node_id UUID,
    site_id UUID,
    hostname LowCardinality(String),
    method LowCardinality(String),
    scheme LowCardinality(String),
    protocol LowCardinality(String),
    path String,
    query_string String,
    client_ip IPv6,
    status_code UInt16,
    request_header_bytes UInt64,
    request_body_bytes UInt64,
    response_header_bytes UInt64,
    response_body_bytes UInt64,
    ingress_bytes UInt64 MATERIALIZED request_header_bytes + request_body_bytes,
    egress_bytes UInt64 MATERIALIZED response_header_bytes + response_body_bytes,
    duration_us UInt64,
    upstream_address String,
    upstream_status UInt16,
    cache_status LowCardinality(String),
    content_type LowCardinality(String),
    file_extension LowCardinality(String),
    referer String,
    user_agent String,
    country LowCardinality(String),
    region LowCardinality(String)
)
ENGINE = MergeTree
PARTITION BY toDate(event_time)
ORDER BY (cluster_id, site_id, event_time, node_id)
TTL event_time + INTERVAL 7 DAY DELETE
SETTINGS index_granularity = 8192;

-- Reusable cluster/node/site usage cube. UUID zero can be used by later
-- rollups when a dimension is intentionally collapsed.
CREATE TABLE IF NOT EXISTS goveto.request_usage_hourly
(
    bucket DateTime('UTC'),
    cluster_id UUID,
    node_id UUID,
    site_id UUID,
    requests SimpleAggregateFunction(sum, UInt64),
    ingress_bytes SimpleAggregateFunction(sum, UInt64),
    egress_bytes SimpleAggregateFunction(sum, UInt64),
    cache_hit_requests SimpleAggregateFunction(sum, UInt64),
    cache_miss_requests SimpleAggregateFunction(sum, UInt64),
    cache_egress_bytes SimpleAggregateFunction(sum, UInt64),
    origin_requests SimpleAggregateFunction(sum, UInt64),
    error_requests SimpleAggregateFunction(sum, UInt64),
    duration_us_sum SimpleAggregateFunction(sum, UInt64),
    duration_count SimpleAggregateFunction(sum, UInt64),
    unique_ip_state AggregateFunction(uniqCombined64, IPv6)
)
ENGINE = AggregatingMergeTree
PARTITION BY toDate(bucket)
ORDER BY (cluster_id, site_id, node_id, bucket)
TTL bucket + INTERVAL 48 HOUR DELETE;

CREATE MATERIALIZED VIEW IF NOT EXISTS goveto.mv_request_usage_hourly
TO goveto.request_usage_hourly
AS
SELECT
    toStartOfHour(event_time) AS bucket,
    cluster_id,
    node_id,
    site_id,
    count() AS requests,
    sum(ingress_bytes) AS ingress_bytes,
    sum(egress_bytes) AS egress_bytes,
    countIf(cache_status IN ('hit', 'HIT')) AS cache_hit_requests,
    countIf(cache_status IN ('miss', 'MISS', 'bypass', 'BYPASS')) AS cache_miss_requests,
    sumIf(egress_bytes, cache_status IN ('hit', 'HIT')) AS cache_egress_bytes,
    countIf(upstream_address != '') AS origin_requests,
    countIf(status_code >= 500) AS error_requests,
    sum(duration_us) AS duration_us_sum,
    count() AS duration_count,
    uniqCombined64State(client_ip) AS unique_ip_state
FROM goveto.web_request_logs
GROUP BY bucket, cluster_id, node_id, site_id;

CREATE TABLE IF NOT EXISTS goveto.request_usage_daily
(
    bucket Date,
    cluster_id UUID,
    node_id UUID,
    site_id UUID,
    requests SimpleAggregateFunction(sum, UInt64),
    ingress_bytes SimpleAggregateFunction(sum, UInt64),
    egress_bytes SimpleAggregateFunction(sum, UInt64),
    cache_hit_requests SimpleAggregateFunction(sum, UInt64),
    cache_miss_requests SimpleAggregateFunction(sum, UInt64),
    cache_egress_bytes SimpleAggregateFunction(sum, UInt64),
    origin_requests SimpleAggregateFunction(sum, UInt64),
    error_requests SimpleAggregateFunction(sum, UInt64),
    duration_us_sum SimpleAggregateFunction(sum, UInt64),
    duration_count SimpleAggregateFunction(sum, UInt64),
    unique_ip_state AggregateFunction(uniqCombined64, IPv6)
)
ENGINE = AggregatingMergeTree
PARTITION BY toYYYYMM(bucket)
ORDER BY (cluster_id, site_id, node_id, bucket)
TTL bucket + INTERVAL 31 DAY DELETE;

-- Low-cardinality breakdowns share one table. Each request expands only into
-- bounded dimensions, avoiding path/IP/referer write amplification here.
CREATE TABLE IF NOT EXISTS goveto.request_breakdown_hourly
(
    bucket DateTime('UTC'),
    cluster_id UUID,
    site_id UUID,
    dimension LowCardinality(String),
    value String,
    requests UInt64,
    ingress_bytes UInt64,
    egress_bytes UInt64
)
ENGINE = SummingMergeTree
PARTITION BY toDate(bucket)
ORDER BY (cluster_id, site_id, dimension, value, bucket)
TTL bucket + INTERVAL 48 HOUR DELETE;

CREATE MATERIALIZED VIEW IF NOT EXISTS goveto.mv_request_breakdown_hourly
TO goveto.request_breakdown_hourly
AS
SELECT
    toStartOfHour(event_time) AS bucket,
    cluster_id,
    site_id,
    dim_tuple.1 AS dimension,
    dim_tuple.2 AS value,
    count() AS requests,
    sum(ingress_bytes) AS ingress_bytes,
    sum(egress_bytes) AS egress_bytes
FROM goveto.web_request_logs
ARRAY JOIN
[
    tuple('method', toString(method)),
    tuple('status_code', toString(status_code)),
    tuple('protocol', toString(protocol)),
    tuple('cache_status', toString(cache_status)),
    tuple('file_extension', toString(file_extension)),
    tuple('content_type', toString(content_type)),
    tuple('country', toString(country)),
    tuple('region', toString(region))
] AS dim_tuple
GROUP BY bucket, cluster_id, site_id, dim_tuple.1, dim_tuple.2;

CREATE TABLE IF NOT EXISTS goveto.request_breakdown_daily
AS goveto.request_breakdown_hourly
ENGINE = SummingMergeTree
PARTITION BY toYYYYMM(toDate(bucket))
ORDER BY (cluster_id, site_id, dimension, value, bucket)
TTL bucket + INTERVAL 31 DAY DELETE;

-- High-cardinality dimensions stay isolated so their merge and Top-N query
-- costs do not affect normal method/status dashboards.
CREATE TABLE IF NOT EXISTS goveto.request_path_hourly
(
    bucket DateTime('UTC'), cluster_id UUID, site_id UUID, path String,
    requests UInt64, ingress_bytes UInt64, egress_bytes UInt64
)
ENGINE = SummingMergeTree
PARTITION BY toDate(bucket)
ORDER BY (cluster_id, site_id, bucket, path)
TTL bucket + INTERVAL 48 HOUR DELETE;

CREATE MATERIALIZED VIEW IF NOT EXISTS goveto.mv_request_path_hourly
TO goveto.request_path_hourly AS
SELECT toStartOfHour(event_time) bucket, cluster_id, site_id, path,
       count() requests, sum(ingress_bytes) ingress_bytes, sum(egress_bytes) egress_bytes
FROM goveto.web_request_logs
GROUP BY bucket, cluster_id, site_id, path;

CREATE TABLE IF NOT EXISTS goveto.request_ip_hourly
(
    bucket DateTime('UTC'), cluster_id UUID, site_id UUID, client_ip IPv6,
    requests UInt64, ingress_bytes UInt64, egress_bytes UInt64
)
ENGINE = SummingMergeTree
PARTITION BY toDate(bucket)
ORDER BY (cluster_id, site_id, bucket, client_ip)
TTL bucket + INTERVAL 48 HOUR DELETE;

CREATE MATERIALIZED VIEW IF NOT EXISTS goveto.mv_request_ip_hourly
TO goveto.request_ip_hourly AS
SELECT toStartOfHour(event_time) bucket, cluster_id, site_id, client_ip,
       count() requests, sum(ingress_bytes) ingress_bytes, sum(egress_bytes) egress_bytes
FROM goveto.web_request_logs
GROUP BY bucket, cluster_id, site_id, client_ip;

CREATE TABLE IF NOT EXISTS goveto.request_hostname_hourly
(
    bucket DateTime('UTC'), cluster_id UUID, site_id UUID, hostname String,
    requests UInt64, ingress_bytes UInt64, egress_bytes UInt64
)
ENGINE = SummingMergeTree
PARTITION BY toDate(bucket)
ORDER BY (cluster_id, site_id, bucket, hostname)
TTL bucket + INTERVAL 48 HOUR DELETE;

CREATE MATERIALIZED VIEW IF NOT EXISTS goveto.mv_request_hostname_hourly
TO goveto.request_hostname_hourly AS
SELECT toStartOfHour(event_time) bucket, cluster_id, site_id, hostname,
       count() requests, sum(ingress_bytes) ingress_bytes, sum(egress_bytes) egress_bytes
FROM goveto.web_request_logs
GROUP BY bucket, cluster_id, site_id, hostname;

CREATE TABLE IF NOT EXISTS goveto.request_referer_hourly
(
    bucket DateTime('UTC'), cluster_id UUID, site_id UUID, referer String,
    requests UInt64, ingress_bytes UInt64, egress_bytes UInt64
)
ENGINE = SummingMergeTree
PARTITION BY toDate(bucket)
ORDER BY (cluster_id, site_id, bucket, cityHash64(referer), referer)
TTL bucket + INTERVAL 48 HOUR DELETE;

CREATE MATERIALIZED VIEW IF NOT EXISTS goveto.mv_request_referer_hourly
TO goveto.request_referer_hourly AS
SELECT toStartOfHour(event_time) bucket, cluster_id, site_id, referer,
       count() requests, sum(ingress_bytes) ingress_bytes, sum(egress_bytes) egress_bytes
FROM goveto.web_request_logs
WHERE referer != ''
GROUP BY bucket, cluster_id, site_id, referer;

-- Compact daily storage for long-range high-cardinality Top-N reports.
CREATE TABLE IF NOT EXISTS goveto.request_high_cardinality_daily
(
    bucket Date,
    cluster_id UUID,
    site_id UUID,
    dimension LowCardinality(String),
    value String,
    requests UInt64,
    ingress_bytes UInt64,
    egress_bytes UInt64
)
ENGINE = SummingMergeTree
PARTITION BY toYYYYMM(bucket)
ORDER BY (cluster_id, site_id, dimension, bucket, cityHash64(value), value)
TTL bucket + INTERVAL 31 DAY DELETE;

-- Agent-reported host metrics remain independent from HTTP request analytics.
CREATE TABLE IF NOT EXISTS goveto.node_runtime_metrics_minute
(
    minute DateTime('UTC'), cluster_id UUID, node_id UUID,
    cpu_usage_percent Float32,
    memory_used_bytes UInt64, memory_total_bytes UInt64,
    load_1 Float32, load_5 Float32, load_15 Float32,
    cache_used_bytes UInt64, cache_max_bytes UInt64, cache_directory String,
    disk_used_bytes UInt64, disk_total_bytes UInt64
)
ENGINE = ReplacingMergeTree
PARTITION BY toYYYYMM(minute)
ORDER BY (node_id, minute)
TTL minute + INTERVAL 90 DAY DELETE;

CREATE TABLE IF NOT EXISTS goveto.node_traffic_metrics_minute
(
    minute DateTime('UTC'), cluster_id UUID, node_id UUID,
    requests UInt64, ingress_bytes UInt64, egress_bytes UInt64,
    cache_hit_requests UInt64, cache_miss_requests UInt64, origin_requests UInt64
)
ENGINE = SummingMergeTree
PARTITION BY toYYYYMM(minute)
ORDER BY (node_id, minute)
TTL minute + INTERVAL 90 DAY DELETE;
