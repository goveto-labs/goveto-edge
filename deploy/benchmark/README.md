# Edge Agent benchmark environment

This topology keeps the load generator (`0-1`) and Edge Agent (`2-3`) on separate CPU sets. The origin, Redis, and mock mTLS Gateway use the remaining example CPUs. Adjust all `cpuset` values to the fixed Linux runner before collecting capacity data.

Generate disposable benchmark mTLS credentials and the initial H1/H2/H3 site task:

```sh
go run ./cmd/agent-bench-pki --output deploy/benchmark/state
docker compose -f deploy/benchmark/compose.yaml up -d origin redis gateway agent
```

Record the node hardware profile before a capacity run:

```sh
docker compose -f deploy/benchmark/compose.yaml exec agent edge-agent benchmark --directory /opt/goveto-edge/cache
```

Run the capacity example (H3 over UDP) and write `report.json`, `summary.md`, and `timeseries.csv` under `deploy/benchmark/results`:

```sh
docker compose -f deploy/benchmark/compose.yaml --profile run run --rm load
```

Run the H1/H2/H3 capacity matrix across concurrency levels `1,8,32,128,512`:

```sh
script/run_agent_benchmark_matrix.sh
```

The default matrix has 15 cases. The extended origin matrix adds 1 KiB, 16 KiB, and 1 MiB payloads plus reused and newly established connections:

```sh
script/run_agent_benchmark_matrix.sh --full-origin
```

## Fast 26 CPU workflow

Do not run every combination with capacity timings. Screen the full origin matrix
first; its 90 cases take about 9 minutes of measurement time instead of 18 hours:

```sh
script/run_agent_benchmark_matrix.sh --full-origin --quick --runner 26c-agent8
```

Run the focused CDN smoke suite next. It covers cache hits, sustained misses,
cold-object request coalescing, eviction, ranges, 16 MiB transfers, multiple domains and origins, slow and failed
origins, an 8 MiB/s shaped origin, and request rate limiting:

```sh
script/run_agent_cdn_benchmark.sh --profile smoke --runner 26c-agent8
```

Use capacity timings only for the passing protocols and the concurrency values
around the observed knee. This example confirms two concurrency levels in about
one hour of measurement time:

```sh
script/run_agent_benchmark_matrix.sh \
  --runner 26c-agent2 --suite capacity --protocols "h1 h2 h3" \
  --sizes "1024 16384 1048576" --connection-modes "reuse" \
  --concurrencies "32 128" --warmup 10s --duration 60s --repeats 3
```

Confirm selected CDN behaviors independently. A full focused capacity invocation
usually takes 45-100 minutes depending on selected groups:

```sh
script/run_agent_cdn_benchmark.sh --profile capacity --runner 26c-agent2 \
  --scenarios "cache-hit cache-miss range large-transfer multi-origin origin-resilience"
```

Soak is deliberately separate and defaults to one H2, 1 MiB cache-hit case for
six hours. Select more protocols explicitly; each protocol adds another six-hour
serial case:

```sh
script/run_agent_cdn_benchmark.sh --profile soak --runner 26c-agent2
```

The 26 CPU runner has `agent2`, `agent4`, and `agent8` layouts. All give the load
generator and origins enough dedicated CPU to avoid the old single-core origin
bottleneck. Use `agent2` to reproduce the current node size, then confirm only
the capacity knee with `agent4` and `agent8` to measure scaling efficiency.
Capacity cases remain serial: running multiple load generators against the same Agent would make CPU,
disk, network, and latency results unsuitable as a per-node baseline.

The benchmark PKI config creates separate hosts so the original
`benchmark.example.test` pure-origin matrix remains uncached. The cache site uses
a fixed 8 MiB cache to make eviction observable in a short run. Multi-origin
reports capture `X-Benchmark-Origin` distributions; cache reports include
per-run hit, miss, and eviction deltas.

Before H3 runs on Linux, raise UDP buffers to at least the value requested by
quic-go (normally 7.5 MB) and confirm the warning is absent. A smoke run is a
functional screen, not an architecture baseline; freeze baselines only from the
fixed Linux runner using confirmed capacity cases.

Use `--suite pr` for a shorter functional run or `--dry-run` to inspect the expanded cases. Each invocation writes an isolated timestamped directory containing `hardware.json`, `matrix.tsv`, per-case logs, and the normal JSON/Markdown/CSV reports. By default the script resets only the Compose project's benchmark volumes to guarantee a fresh Agent identity and configuration; pass `--reuse-environment` to retain them.

H1, H2, and H3 all use `https://agent:8444/bytes/16384`; select the transport with `--protocol`. Confirm `summary.negotiated_protocol` in every report. Capacity defaults are a 30 second warmup, 120 second measurement, and five repetitions. Run concurrency values `1,8,32,128,512` separately and stop once errors exceed 0.1%, p99 loses control, or Agent CPU remains above 90%.

The mock Gateway uses real TLS 1.3 client authentication and the production JSON gRPC stream. Its `--ack-delay` and `--reject-logs` options cover delayed and rejected log uploads without requiring PostgreSQL or ClickHouse. Credentials in `state/` are disposable and must not be committed.

The Compose environment explicitly enables the benchmark-only telemetry listener. It supplies heap, allocation rate, GC, goroutine, log queue, dropped-log, and cache counters to the load generator; the listener is absent unless `EDGE_AGENT_BENCHMARK_LISTEN` is set. The load container joins only the Agent PID namespace so it can also sample Agent CPU, RSS, FDs, connections, and disk I/O while remaining pinned to separate CPUs.

Fixed-runner results are comparable only within the same architecture. Collect ten trend-only runs before freezing architecture-specific baselines. A baseline update is a reviewed artifact change; the benchmark command never accepts a new baseline automatically.
