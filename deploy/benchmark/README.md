# Edge Agent benchmark

Benchmark testing has one shell entry point:

```sh
script/run_agent_benchmark.sh quick [options]
script/run_agent_benchmark.sh full [options]
script/run_agent_benchmark.sh bandwidth --runner 26c-agent4-load10 [options]
script/run_agent_benchmark.sh small-reuse --runner 26c-agent8 [options]
script/run_agent_benchmark.sh cache [options]
```

`quick` runs the complete functional screen. It covers all origin and CDN test
items with short timings, including H1/H2/H3, reused and new connections,
1 KiB/16 KiB/1 MiB payloads, concurrency 1/8/32/128/512, caching, coalescing,
eviction, ranges, 16 MiB transfers, multiple domains and origins, origin
resilience, throttling, and rate limiting. New-connection c512 cases run only
after the matching c128 case passes and are explicitly classified as capacity
probes. It skips only the long Capacity and stability stages.

`full` starts with the same complete screen. It then runs Capacity only for
cases whose screen status is exactly `PASS`, using a 30 second warmup, 120 second
measurement, three repetitions, and focused concurrency 32/128. It next runs
the same complete cache matrix exposed by the standalone `cache` command.
Finally it runs a 15 minute cache-hit preflight followed by the long stability
test (H2 for six hours by default) only after the corresponding Capacity case
passes.

`bandwidth` runs only the 1 MiB reuse c32/c128 and 16 MiB transfer c8 Capacity
cases. It requires `26c-agent4-load10`, keeping this follow-up separate from the
functional and soak matrix.

`small-reuse` runs the 1 KiB and 16 KiB reuse matrix at c32/c128 for every
selected protocol on `26c-agent8`. Each case compares full observability with a
matching control and requires full throughput to reach 90% of control. The c32
full cases additionally require complete, drained access logs.

`cache` is the standalone entry point for the complete cache performance suite
that is also included in `full`. For every selected protocol it
measures hot reads at 1 KiB/16 KiB/1 MiB and c1/c8/c32/c128, unique-key cold
writes, a bounded 256-key mixed workload, fixed range hits, and cold-request
coalescing through c512. It also compares the first, 31st, and fallback routes
of a maximum-size 32-rule policy using extension, prefix, grouped regex, full
cache-key, header-key, and hashed-key settings. H1 additionally exercises disk
eviction under the configured cache capacity. Every case validates cache
telemetry and response behavior, so an origin-only result cannot pass as a
cache measurement.

## Commands

Use the 8-core Agent layout for the complete short screen:

```sh
script/run_agent_benchmark.sh quick --runner 26c-agent8
```

Use the production-sized 2-core Agent layout for the full baseline workflow:

```sh
script/run_agent_benchmark.sh full --runner 26c-agent2
```

Run the dedicated cache matrix, using shorter timings for a calibration pass:

```sh
script/run_agent_benchmark.sh cache --runner 26c-agent4
script/run_agent_benchmark.sh cache --runner 26c-agent4 \
  --cache-warmup 2s --cache-duration 5s --cache-repeats 1
```

Inspect every expanded case without starting Docker:

```sh
script/run_agent_benchmark.sh quick --runner 26c-agent8 --dry-run
script/run_agent_benchmark.sh full --runner 26c-agent2 --dry-run
script/run_agent_benchmark.sh cache --runner 26c-agent4 --dry-run
```

Available runner layouts are `default`, `26c-agent2`, `26c-agent4`,
`26c-agent4-load10`, and `26c-agent8`. The standard 26-CPU layouts keep six CPUs
assigned to the load generator; agent4/agent8 measure Agent scaling but do not
increase load-generator capacity. `26c-agent4-load10` assigns CPUs 0-9 to load,
10-13 to Agent, four CPUs to each origin, and two CPUs each to Redis and Gateway.
Use `bandwidth` on it for the 1 MiB and 16 MiB protocol follow-up when standard
agent4 results are load-saturated; those standard results are lower bounds, not
load10 baselines.
Use `--protocols "h1 h2 h3"` to select protocols, `--run-id NAME` to name the
result directory, `--baseline-run RUN_ID` to compare against the matching case
from a prior run, and `--cleanup` to stop containers after the run. Baseline
new reports use schema 1.4. Baseline comparison accepts compatible schema 1.2,
1.3, and 1.4 reports and requires an exact runner, architecture, suite,
scenario, protocol, concurrency, and connection-mode match. Run
`script/run_agent_benchmark.sh --help` for all options.

The cache matrix timings in `full` and `cache` default to a 5 second warmup,
15 second measurement, and three repetitions. Override them with
`--cache-warmup`, `--cache-duration`, and `--cache-repeats`. The coalescing and
eviction cases intentionally use one repeat because they depend on a freshly
cold key or cache-capacity transition.

## Result validity

Each case is classified as:

- `PASS`: eligible for Capacity or baseline use.
- `PRODUCT_FAIL`: request or product behavior failed.
- `LOAD_SATURATED`: the load generator exceeded `--max-load-cpu` (85% by
  default), so throughput is only a lower bound and is not a product failure.
- `TARGET_SATURATED`: an explicit c512 capacity probe exceeded the target's
  transport capacity after the c128 compatibility gate passed. HTTP, integrity,
  and cleanup errors remain product failures.
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

Cache-hit Capacity and soak cases accept `HIT` and `STALE`, reject `MISS`, and
limit `STALE` to 1% per repetition. Cache-miss keys include a per-run and
per-repeat namespace. High-concurrency cases close their load-side transports
before a 60 second cooldown. Natural cooldown values remain in the report, then
the benchmark-only telemetry endpoint records a separate post-GC/scavenge RSS
and heap sample. Cache eviction gates RSS growth from the case baseline instead
of absolute process RSS. Rate-limit screening requires every
measured response to be 429. Its 120 second Capacity case accepts only 200/429,
requires at least one 429, and permits at most 200 successful responses per
repetition. Every HTTP status is counted in schema 1.4 reports. Cache reports
also include write queue depth and bytes, queue rejections, batches, committed
objects, average batch size, commit latency, inflight writes, total allocation,
and allocated bytes per request. A run invalidated only by the fixed 5% RPS CV
threshold is repeated once with identical settings.

The focused cache matrix drains and resets the cache before every cold-write
and mixed case. Cold c32/c128 cases require at least 200 RPS with p99 below one
second; mixed c32/c128 cases require at least 1,000 RPS with p99 below one
second. All cache cases require the write queue to drain without rejections.
Direct `agent-bench` runs can add `--min-rps`, `--max-p99`,
`--max-allocation-bytes-per-request`, `--require-cache-writes-drained`,
`--min-baseline-rps-ratio`, and `--max-baseline-allocation-ratio` gates.

## H3 prerequisite

Before H3 testing on Linux, set both UDP buffers to at least 7,500,000 bytes:

```sh
sudo sysctl -w net.core.rmem_max=7500000
sudo sysctl -w net.core.wmem_max=7500000
```

The entry script checks these values in the Agent network namespace and rejects
quic-go receive/send buffer warnings as `ENV_INVALID`. H3 new-connection load
shares one UDP socket while still creating a fresh QUIC connection per request.
The 1 KiB matrix also adds a reuse c512 probe after reuse c128 passes.

## Environment

The runner creates disposable benchmark mTLS credentials, builds the origin,
Gateway, Agent, and load images, and starts the Compose environment itself. By
default it resets only this Compose project's benchmark volumes. Pass
`--reuse-environment` to retain an existing benchmark identity and configuration.

The mock Gateway uses TLS 1.3 client authentication and the production JSON gRPC
stream. The benchmark-only telemetry listener exposes Agent CPU, memory, disk,
connection, cache, heap, GC, goroutine, and log-queue measurements to the load
container while the load generator remains pinned to separate CPUs. Its
`POST /cache/drain` and `POST /cache/reset` controls are available only on this
listener and are not part of the product HTTP API.

Fixed-runner results are comparable only within the same runner and architecture.
Capacity cases run serially so shared CPU, disk, network, and latency
measurements remain usable as a per-node baseline.
