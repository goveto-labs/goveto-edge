# Edge Agent benchmark

Benchmark testing has one shell entry point:

```sh
script/run_agent_benchmark.sh quick [options]
script/run_agent_benchmark.sh full [options]
```

`quick` runs the complete functional screen. It covers all origin and CDN test
items with short timings, including H1/H2/H3, reused and new connections,
1 KiB/16 KiB/1 MiB payloads, concurrency 1/8/32/128/512, caching, coalescing,
eviction, ranges, 16 MiB transfers, multiple domains and origins, origin
resilience, throttling, and rate limiting. It skips only the long Capacity and
stability stages.

`full` starts with the same complete screen. It then runs Capacity only for
cases whose screen status is exactly `PASS`, using a 30 second warmup, 120 second
measurement, three repetitions, and focused concurrency 32/128. Finally it runs
the long 1 MiB cache-hit stability test (H2 for six hours by default) only after
the corresponding Capacity case passes.

## Commands

Use the 8-core Agent layout for the complete short screen:

```sh
script/run_agent_benchmark.sh quick --runner 26c-agent8
```

Use the production-sized 2-core Agent layout for the full baseline workflow:

```sh
script/run_agent_benchmark.sh full --runner 26c-agent2
```

Inspect every expanded case without starting Docker:

```sh
script/run_agent_benchmark.sh quick --runner 26c-agent8 --dry-run
script/run_agent_benchmark.sh full --runner 26c-agent2 --dry-run
```

Available runner layouts are `default`, `26c-agent2`, `26c-agent4`, and
`26c-agent8`. The 26-CPU layouts keep six CPUs assigned to the load generator;
agent4/agent8 measure Agent scaling but do not increase load-generator capacity.
Use `--protocols "h1 h2 h3"` to select protocols, `--run-id NAME` to name the
result directory, and `--cleanup` to stop containers after the run. Run
`script/run_agent_benchmark.sh --help` for all options.

## Result validity

Each case is classified as:

- `PASS`: eligible for Capacity or baseline use.
- `PRODUCT_FAIL`: request or product behavior failed.
- `LOAD_SATURATED`: the load generator exceeded `--max-load-cpu` (85% by
  default), so throughput is only a lower bound and is not a product failure.
- `ENV_INVALID`: the environment or measurement was invalid.

For a 1 MiB `LOAD_SATURATED` result, rerun on a reviewed agent4/agent8 layout or
with a larger or isolated load generator. Change `--max-load-cpu` only when the
runner-specific limit is understood, for example:

```sh
script/run_agent_benchmark.sh full --runner 26c-agent8 --max-load-cpu 90
```

Every run writes an isolated directory under `deploy/benchmark/results/` with
the matrix, JSON/Markdown/CSV reports, per-service logs, resolved Compose and
image details, Git state, Agent binary SHA-256, environment information, and
complete error counts by type.

## H3 prerequisite

Before H3 testing on Linux, set both UDP buffers to at least 7,500,000 bytes:

```sh
sudo sysctl -w net.core.rmem_max=7500000
sudo sysctl -w net.core.wmem_max=7500000
```

The entry script checks these values in the Agent network namespace and rejects
quic-go receive/send buffer warnings as `ENV_INVALID`. H3 new-connection cases
restart the Agent before and after measurement to isolate connection cleanup.

## Environment

The runner creates disposable benchmark mTLS credentials, builds the origin,
Gateway, Agent, and load images, and starts the Compose environment itself. By
default it resets only this Compose project's benchmark volumes. Pass
`--reuse-environment` to retain an existing benchmark identity and configuration.

The mock Gateway uses TLS 1.3 client authentication and the production JSON gRPC
stream. The benchmark-only telemetry listener exposes Agent CPU, memory, disk,
connection, cache, heap, GC, goroutine, and log-queue measurements to the load
container while the load generator remains pinned to separate CPUs.

Fixed-runner results are comparable only within the same architecture. Capacity
cases run serially so shared CPU, disk, network, and latency measurements remain
usable as a per-node baseline.
