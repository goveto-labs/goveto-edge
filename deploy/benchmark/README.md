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

Use `--suite pr` for a shorter functional run or `--dry-run` to inspect the expanded cases. Each invocation writes an isolated timestamped directory containing `hardware.json`, `matrix.tsv`, per-case logs, and the normal JSON/Markdown/CSV reports. By default the script resets only the Compose project's benchmark volumes to guarantee a fresh Agent identity and configuration; pass `--reuse-environment` to retain them.

H1, H2, and H3 all use `https://agent:8444/bytes/16384`; select the transport with `--protocol`. Confirm `summary.negotiated_protocol` in every report. Capacity defaults are a 30 second warmup, 120 second measurement, and five repetitions. Run concurrency values `1,8,32,128,512` separately and stop once errors exceed 0.1%, p99 loses control, or Agent CPU remains above 90%.

The mock Gateway uses real TLS 1.3 client authentication and the production JSON gRPC stream. Its `--ack-delay` and `--reject-logs` options cover delayed and rejected log uploads without requiring PostgreSQL or ClickHouse. Credentials in `state/` are disposable and must not be committed.

The Compose environment explicitly enables the benchmark-only telemetry listener. It supplies heap, allocation rate, GC, goroutine, log queue, dropped-log, and cache counters to the load generator; the listener is absent unless `EDGE_AGENT_BENCHMARK_LISTEN` is set. The load container joins only the Agent PID namespace so it can also sample Agent CPU, RSS, FDs, connections, and disk I/O while remaining pinned to separate CPUs.

Fixed-runner results are comparable only within the same architecture. Collect ten trend-only runs before freezing architecture-specific baselines. A baseline update is a reviewed artifact change; the benchmark command never accepts a new baseline automatically.
