ALTER TABLE analytics.node_runtime_metrics_minute
    ADD COLUMN IF NOT EXISTS cache_stream_encode_drops bigint NOT NULL DEFAULT 0;
