ALTER TABLE analytics.web_request_logs
    ADD COLUMN IF NOT EXISTS isp text NOT NULL DEFAULT '';

CREATE MATERIALIZED VIEW analytics.request_isp_hourly
WITH (timescaledb.continuous) AS
SELECT time_bucket(INTERVAL '1 hour', event_time) AS bucket, cluster_id, site_id, isp AS value,
    count(*)::bigint AS requests,
    sum(request_header_bytes + request_body_bytes)::bigint AS ingress_bytes,
    sum(response_header_bytes + response_body_bytes)::bigint AS egress_bytes
FROM analytics.web_request_logs
GROUP BY 1, 2, 3, 4 WITH NO DATA;

CREATE MATERIALIZED VIEW analytics.request_isp_daily
WITH (timescaledb.continuous) AS
SELECT time_bucket(INTERVAL '1 day', event_time) AS bucket, cluster_id, site_id, isp AS value,
    count(*)::bigint AS requests,
    sum(request_header_bytes + request_body_bytes)::bigint AS ingress_bytes,
    sum(response_header_bytes + response_body_bytes)::bigint AS egress_bytes
FROM analytics.web_request_logs
GROUP BY 1, 2, 3, 4 WITH NO DATA;

CREATE MATERIALIZED VIEW analytics.waf_hits_hourly
WITH (timescaledb.continuous) AS
SELECT time_bucket(INTERVAL '1 hour', event_time) AS bucket, cluster_id, site_id,
    count(*)::bigint AS hits
FROM analytics.web_request_logs
WHERE waf_action <> ''
GROUP BY 1, 2, 3 WITH NO DATA;

CREATE MATERIALIZED VIEW analytics.waf_hits_daily
WITH (timescaledb.continuous) AS
SELECT time_bucket(INTERVAL '1 day', event_time) AS bucket, cluster_id, site_id,
    count(*)::bigint AS hits
FROM analytics.web_request_logs
WHERE waf_action <> ''
GROUP BY 1, 2, 3 WITH NO DATA;

ALTER MATERIALIZED VIEW analytics.request_isp_hourly SET (timescaledb.materialized_only = false);
ALTER MATERIALIZED VIEW analytics.request_isp_daily SET (timescaledb.materialized_only = false);
ALTER MATERIALIZED VIEW analytics.waf_hits_hourly SET (timescaledb.materialized_only = false);
ALTER MATERIALIZED VIEW analytics.waf_hits_daily SET (timescaledb.materialized_only = false);

SELECT add_retention_policy('analytics.request_isp_hourly', INTERVAL '48 hours', if_not_exists => TRUE);
SELECT add_retention_policy('analytics.request_isp_daily', INTERVAL '31 days', if_not_exists => TRUE);
SELECT add_retention_policy('analytics.waf_hits_hourly', INTERVAL '48 hours', if_not_exists => TRUE);
SELECT add_retention_policy('analytics.waf_hits_daily', INTERVAL '31 days', if_not_exists => TRUE);
