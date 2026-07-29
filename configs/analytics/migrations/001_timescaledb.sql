CREATE SCHEMA IF NOT EXISTS analytics;

CREATE TABLE analytics.web_request_logs (
    event_time timestamptz NOT NULL,
    source_log_id bigint NOT NULL,
    request_id text NOT NULL DEFAULT '',
    cluster_id uuid NOT NULL,
    node_id uuid NOT NULL,
    site_id uuid NOT NULL,
    config_version bigint NOT NULL DEFAULT 0,
    hostname text NOT NULL DEFAULT '',
    method text NOT NULL DEFAULT '',
    scheme text NOT NULL DEFAULT '',
    protocol text NOT NULL DEFAULT '',
    path text NOT NULL DEFAULT '',
    query_string text NOT NULL DEFAULT '',
    client_ip inet NOT NULL,
    status_code smallint NOT NULL,
    request_header_bytes bigint NOT NULL,
    request_body_bytes bigint NOT NULL,
    response_header_bytes bigint NOT NULL,
    response_body_bytes bigint NOT NULL,
    duration_us bigint NOT NULL,
    upstream_address text NOT NULL DEFAULT '',
    upstream_status smallint NOT NULL,
    handler_error text NOT NULL DEFAULT '',
    cache_status text NOT NULL DEFAULT '',
    content_type text NOT NULL DEFAULT '',
    file_extension text NOT NULL DEFAULT '',
    referer text NOT NULL DEFAULT '',
    user_agent text NOT NULL DEFAULT '',
    country text NOT NULL DEFAULT '',
    region text NOT NULL DEFAULT '',
    waf_action text NOT NULL DEFAULT '',
    waf_rule_id text NOT NULL DEFAULT '',
    waf_source text NOT NULL DEFAULT '',
    waf_match text NOT NULL DEFAULT '',
    waf_tags text NOT NULL DEFAULT '',
    CONSTRAINT web_request_logs_source_key UNIQUE (event_time, node_id, source_log_id)
);
SELECT create_hypertable('analytics.web_request_logs', 'event_time',
    chunk_time_interval => INTERVAL '1 hour', if_not_exists => TRUE);
CREATE INDEX web_request_logs_cluster_site_time_idx
    ON analytics.web_request_logs (cluster_id, site_id, event_time DESC);
CREATE INDEX web_request_logs_cluster_node_time_idx
    ON analytics.web_request_logs (cluster_id, node_id, event_time DESC);
CREATE INDEX web_request_logs_waf_idx
    ON analytics.web_request_logs (cluster_id, site_id, event_time DESC)
    WHERE waf_action <> '';
ALTER TABLE analytics.web_request_logs SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'cluster_id,site_id,node_id',
    timescaledb.compress_orderby = 'event_time DESC'
);
SELECT add_compression_policy('analytics.web_request_logs', INTERVAL '6 hours', if_not_exists => TRUE);

CREATE TABLE analytics.node_runtime_metrics_minute (
    minute timestamptz NOT NULL, cluster_id uuid NOT NULL, node_id uuid NOT NULL,
    cpu_usage_percent real NOT NULL, memory_used_bytes bigint NOT NULL, memory_total_bytes bigint NOT NULL,
    load_1 real NOT NULL, load_5 real NOT NULL, load_15 real NOT NULL, connections bigint NOT NULL,
    cache_used_bytes bigint NOT NULL, cache_directory text NOT NULL DEFAULT '',
    cache_entries bigint NOT NULL, cache_hits bigint NOT NULL, cache_misses bigint NOT NULL,
    cache_stale_hits bigint NOT NULL, cache_evictions bigint NOT NULL,
    cache_rejected_writes bigint NOT NULL, cache_corruptions bigint NOT NULL,
    cache_hit_rate real NOT NULL, cache_capacity_ratio real NOT NULL,
    cache_alerts text[] NOT NULL DEFAULT '{}', disk_used_bytes bigint NOT NULL, disk_total_bytes bigint NOT NULL,
    PRIMARY KEY (minute, node_id)
);
SELECT create_hypertable('analytics.node_runtime_metrics_minute', 'minute',
    chunk_time_interval => INTERVAL '1 day', if_not_exists => TRUE);
CREATE INDEX node_runtime_cluster_time_idx ON analytics.node_runtime_metrics_minute (cluster_id, minute DESC);
SELECT add_retention_policy('analytics.node_runtime_metrics_minute', INTERVAL '90 days', if_not_exists => TRUE);

CREATE TABLE analytics.origin_health_metrics_minute (
    minute timestamptz NOT NULL, cluster_id uuid NOT NULL, node_id uuid NOT NULL, site_id uuid NOT NULL,
    origin_address text NOT NULL, healthy boolean NOT NULL, available boolean NOT NULL,
    fails integer NOT NULL, requests bigint NOT NULL, errors bigint NOT NULL,
    average_latency_ms double precision NOT NULL, error_rate double precision NOT NULL,
    PRIMARY KEY (minute, node_id, site_id, origin_address)
);
SELECT create_hypertable('analytics.origin_health_metrics_minute', 'minute',
    chunk_time_interval => INTERVAL '1 day', if_not_exists => TRUE);
CREATE INDEX origin_health_cluster_site_time_idx
    ON analytics.origin_health_metrics_minute (cluster_id, site_id, minute DESC);
SELECT add_retention_policy('analytics.origin_health_metrics_minute', INTERVAL '31 days', if_not_exists => TRUE);

CREATE TABLE analytics.daily_unique_ips (
    day date NOT NULL, cluster_id uuid NOT NULL, site_id uuid NOT NULL, client_ip inet NOT NULL,
    PRIMARY KEY (day, cluster_id, site_id, client_ip)
);
SELECT create_hypertable('analytics.daily_unique_ips', 'day',
    chunk_time_interval => INTERVAL '7 days', if_not_exists => TRUE);
CREATE INDEX daily_unique_ips_cluster_day_idx ON analytics.daily_unique_ips (cluster_id, day);

CREATE MATERIALIZED VIEW analytics.request_usage_hourly
WITH (timescaledb.continuous) AS
SELECT time_bucket(INTERVAL '1 hour', event_time) AS bucket, cluster_id, node_id, site_id,
    count(*)::bigint AS requests,
    sum(request_header_bytes + request_body_bytes)::bigint AS ingress_bytes,
    sum(response_header_bytes + response_body_bytes)::bigint AS egress_bytes,
    count(*) FILTER (WHERE upper(cache_status) = 'HIT') AS cache_hit_requests,
    count(*) FILTER (WHERE upper(cache_status) IN ('MISS', 'BYPASS')) AS cache_miss_requests,
    coalesce(sum(response_header_bytes + response_body_bytes) FILTER (WHERE upper(cache_status) = 'HIT'), 0)::bigint AS cache_egress_bytes,
    count(*) FILTER (WHERE upstream_address <> '') AS origin_requests,
    count(*) FILTER (WHERE status_code >= 500) AS error_requests,
    sum(duration_us)::bigint AS duration_us_sum
FROM analytics.web_request_logs
GROUP BY 1, 2, 3, 4 WITH NO DATA;

CREATE MATERIALIZED VIEW analytics.request_usage_daily
WITH (timescaledb.continuous) AS
SELECT time_bucket(INTERVAL '1 day', event_time) AS bucket, cluster_id, node_id, site_id,
    count(*)::bigint AS requests,
    sum(request_header_bytes + request_body_bytes)::bigint AS ingress_bytes,
    sum(response_header_bytes + response_body_bytes)::bigint AS egress_bytes,
    count(*) FILTER (WHERE upper(cache_status) = 'HIT') AS cache_hit_requests,
    count(*) FILTER (WHERE upper(cache_status) IN ('MISS', 'BYPASS')) AS cache_miss_requests,
    coalesce(sum(response_header_bytes + response_body_bytes) FILTER (WHERE upper(cache_status) = 'HIT'), 0)::bigint AS cache_egress_bytes,
    count(*) FILTER (WHERE upstream_address <> '') AS origin_requests,
    count(*) FILTER (WHERE status_code >= 500) AS error_requests,
    sum(duration_us)::bigint AS duration_us_sum
FROM analytics.web_request_logs
GROUP BY 1, 2, 3, 4 WITH NO DATA;

CREATE MATERIALIZED VIEW analytics.node_traffic_metrics_minute
WITH (timescaledb.continuous) AS
SELECT time_bucket(INTERVAL '1 minute', event_time) AS minute, cluster_id, node_id,
    count(*)::bigint AS requests,
    sum(request_header_bytes + request_body_bytes)::bigint AS ingress_bytes,
    sum(response_header_bytes + response_body_bytes)::bigint AS egress_bytes,
    coalesce(sum(response_header_bytes + response_body_bytes) FILTER (WHERE upper(cache_status) = 'HIT'), 0)::bigint AS cache_egress_bytes,
    count(*) FILTER (WHERE upper(cache_status) = 'HIT') AS cache_hit_requests,
    count(*) FILTER (WHERE upper(cache_status) IN ('MISS', 'BYPASS')) AS cache_miss_requests,
    count(*) FILTER (WHERE upstream_address <> '') AS origin_requests
FROM analytics.web_request_logs
GROUP BY 1, 2, 3 WITH NO DATA;

ALTER MATERIALIZED VIEW analytics.request_usage_hourly
    SET (timescaledb.materialized_only = false);
ALTER MATERIALIZED VIEW analytics.request_usage_daily
    SET (timescaledb.materialized_only = false);
ALTER MATERIALIZED VIEW analytics.node_traffic_metrics_minute
    SET (timescaledb.materialized_only = false);

DO $migration$
DECLARE
    item record;
BEGIN
    FOR item IN SELECT * FROM (VALUES
        ('method', 'method'), ('status', 'status_code'),
        ('extension', 'file_extension'), ('hostname', 'hostname'),
        ('referer', 'referer'), ('path', 'path'), ('client_ip', 'client_ip'),
        ('country', 'country'), ('region', 'region')
    ) AS dimensions(name, expression)
    LOOP
        EXECUTE format(
            'CREATE MATERIALIZED VIEW analytics.request_%I_hourly WITH (timescaledb.continuous) AS '
            'SELECT time_bucket(INTERVAL ''1 hour'', event_time) AS bucket, cluster_id, site_id, %s AS value, '
            'count(*)::bigint AS requests, sum(request_header_bytes + request_body_bytes)::bigint AS ingress_bytes, '
            'sum(response_header_bytes + response_body_bytes)::bigint AS egress_bytes '
            'FROM analytics.web_request_logs GROUP BY 1, 2, 3, 4 WITH NO DATA',
            item.name, item.expression
        );
        EXECUTE format(
            'ALTER MATERIALIZED VIEW analytics.request_%I_hourly SET (timescaledb.materialized_only = false)',
            item.name
        );
        EXECUTE format(
            'CREATE MATERIALIZED VIEW analytics.request_%I_daily WITH (timescaledb.continuous) AS '
            'SELECT time_bucket(INTERVAL ''1 day'', event_time) AS bucket, cluster_id, site_id, %s AS value, '
            'count(*)::bigint AS requests, sum(request_header_bytes + request_body_bytes)::bigint AS ingress_bytes, '
            'sum(response_header_bytes + response_body_bytes)::bigint AS egress_bytes '
            'FROM analytics.web_request_logs GROUP BY 1, 2, 3, 4 WITH NO DATA',
            item.name, item.expression
        );
        EXECUTE format(
            'ALTER MATERIALIZED VIEW analytics.request_%I_daily SET (timescaledb.materialized_only = false)',
            item.name
        );
    END LOOP;
END
$migration$;

DO $policies$
DECLARE
    view_name text;
BEGIN
    FOREACH view_name IN ARRAY ARRAY[
        'request_usage_hourly', 'request_method_hourly', 'request_status_hourly',
        'request_extension_hourly', 'request_hostname_hourly', 'request_referer_hourly',
        'request_path_hourly', 'request_client_ip_hourly', 'request_country_hourly',
        'request_region_hourly'
    ] LOOP
        PERFORM add_retention_policy(('analytics.' || view_name)::regclass, INTERVAL '48 hours', if_not_exists => TRUE);
    END LOOP;
    FOREACH view_name IN ARRAY ARRAY[
        'request_usage_daily', 'request_method_daily', 'request_status_daily',
        'request_extension_daily', 'request_hostname_daily', 'request_referer_daily',
        'request_path_daily', 'request_client_ip_daily', 'request_country_daily',
        'request_region_daily'
    ] LOOP
        PERFORM add_retention_policy(('analytics.' || view_name)::regclass, INTERVAL '31 days', if_not_exists => TRUE);
    END LOOP;
    PERFORM add_retention_policy('analytics.node_traffic_metrics_minute', INTERVAL '90 days', if_not_exists => TRUE);
END
$policies$;

SELECT add_retention_policy('analytics.daily_unique_ips', INTERVAL '32 days', if_not_exists => TRUE);
