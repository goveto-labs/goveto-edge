#!/usr/bin/env bash

set -uo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
readonly COMPOSE_BASE="$REPO_ROOT/deploy/benchmark/compose.yaml"
readonly COMPOSE_26C="$REPO_ROOT/deploy/benchmark/compose.26c.yaml"
readonly COMPOSE_26C_AGENT2="$REPO_ROOT/deploy/benchmark/compose.26c-agent2.yaml"
readonly COMPOSE_26C_AGENT4="$REPO_ROOT/deploy/benchmark/compose.26c-agent4.yaml"
readonly COMPOSE_PROJECT="goveto-edge-cdn-benchmark"
readonly STATE_DIR="$REPO_ROOT/deploy/benchmark/state"
readonly RESULTS_ROOT="$REPO_ROOT/deploy/benchmark/results"

profile="smoke"
protocols="h1 h2 h3"
protocols_set=false
scenarios="cache-hit cache-miss cache-eviction range large-transfer multi-domain multi-origin origin-resilience bandwidth-limit"
run_id="$(date -u +%Y%m%dT%H%M%SZ)-cdn"
runner="default"
reuse_environment=false
cleanup=false
dry_run=false

usage() {
  cat <<'EOF'
Usage: script/run_agent_cdn_benchmark.sh [options]

Options:
  --profile <smoke|capacity|soak>  smoke: 8-15m, capacity: 45-70m, soak: 6h per protocol
  --scenarios "names"              Space-separated scenario groups
  --protocols "h1 h2 h3"           Protocols for protocol-sensitive scenarios
  --runner <name>                  default, 26c-agent2, 26c-agent4, or 26c-agent8
  --run-id <name>                  Result directory name
  --reuse-environment              Keep benchmark volumes and credentials
  --cleanup                        Stop containers after the run
  --dry-run                        Print expanded cases without Docker
  -h, --help                       Show this help

Scenario groups:
  cache-hit cache-miss cache-eviction range large-transfer multi-domain
  multi-origin origin-resilience bandwidth-limit

Capacity runs remain serial so CPU, disk, and network measurements describe one
Agent. Use smoke to screen all behavior, then run capacity only for passing groups.
EOF
}

die() { echo "error: $*" >&2; exit 1; }

while (($# > 0)); do
  case "$1" in
    --profile) profile="${2:-}"; shift 2 ;;
    --scenarios) scenarios="${2:-}"; shift 2 ;;
    --protocols) protocols="${2:-}"; protocols_set=true; shift 2 ;;
    --runner) runner="${2:-}"; shift 2 ;;
    --run-id) run_id="${2:-}"; shift 2 ;;
    --reuse-environment) reuse_environment=true; shift ;;
    --cleanup) cleanup=true; shift ;;
    --dry-run) dry_run=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done

case "$profile" in smoke|capacity|soak) ;; *) die "invalid profile: $profile" ;; esac
[[ "$runner" == "26c" ]] && runner="26c-agent8"
case "$runner" in default|26c-agent2|26c-agent4|26c-agent8) ;; *) die "invalid runner: $runner" ;; esac
[[ "$run_id" =~ ^[A-Za-z0-9._-]+$ ]] || die "invalid run-id"
for protocol in $protocols; do case "$protocol" in h1|h2|h3) ;; *) die "invalid protocol: $protocol" ;; esac; done
for scenario in $scenarios; do
  case "$scenario" in
    cache-hit|cache-miss|cache-eviction|range|large-transfer|multi-domain|multi-origin|origin-resilience|bandwidth-limit) ;;
    *) die "invalid scenario: $scenario" ;;
  esac
done

compose_args=(-p "$COMPOSE_PROJECT" -f "$COMPOSE_BASE")
case "$runner" in
  26c-agent2) compose_args+=(-f "$COMPOSE_26C_AGENT2") ;;
  26c-agent4) compose_args+=(-f "$COMPOSE_26C_AGENT4") ;;
  26c-agent8) compose_args+=(-f "$COMPOSE_26C") ;;
esac
compose() { docker compose "${compose_args[@]}" "$@"; }

case "$profile" in
  smoke) suite="pr"; warmup="2s"; duration="8s"; repeats=1 ;;
  capacity) suite="capacity"; warmup="10s"; duration="60s"; repeats=3 ;;
  soak)
    suite="soak"; warmup="30s"; duration="6h"; repeats=1; scenarios="cache-hit"
    $protocols_set || protocols="h2"
    ;;
esac

sha256_zeros() {
  local size="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    head -c "$size" /dev/zero | sha256sum | awk '{print $1}'
  else
    head -c "$size" /dev/zero | shasum -a 256 | awk '{print $1}'
  fi
}

declare -a case_names=()
declare -a case_args=()
add_case() {
  case_names+=("$1")
  shift
  case_args+=("$(printf '%s\t' "$@")")
}

common_case() {
  local name="$1" protocol="$2" host="$3" path="$4" concurrency="$5"
  shift 5
  add_case "$name" agent-bench run --suite "$suite" --protocol "$protocol" --scenario "$name" \
    --url "https://agent:8444$path" --host "$host" --insecure-skip-verify \
    --concurrency "$concurrency" --warmup "$warmup" --duration "$duration" --repeats "$repeats" \
    --agent-pid 1 --agent-metrics-url http://agent:9900/metrics "$@"
}

has_scenario() { [[ " $scenarios " == *" $1 "* ]]; }

if has_scenario cache-hit; then
  for protocol in $protocols; do
    sizes="1024 16384 1048576"
    [[ "$profile" == "soak" ]] && sizes="1048576"
    for size in $sizes; do
      common_case "cache-hit-${size}b-${protocol}" "$protocol" cache.benchmark.example.test "/bytes/$size?asset=hit-$size" 32 \
        --expected-sha256 "$(sha256_zeros "$size")" --expected-header X-Cache=HIT --capture-header X-Cache --min-cache-hits 1
    done
  done
fi

if has_scenario cache-miss; then
  for protocol in $protocols; do
    common_case "cache-miss-16k-${protocol}" "$protocol" cache.benchmark.example.test /bytes/16384 32 \
      --unique-query --expected-sha256 "$(sha256_zeros 16384)" --expected-header X-Cache=MISS --capture-header X-Cache --min-cache-misses 1
  done
  add_case cache-coalescing-h2 agent-bench run --suite "$suite" --protocol h2 --scenario cache-coalescing-h2 \
    --url "https://agent:8444/delay/100/bytes/16384?asset=coalesce-$run_id" --host cache.benchmark.example.test \
    --insecure-skip-verify --concurrency 128 --skip-warmup --duration "$duration" --repeats 1 \
    --expected-sha256 "$(sha256_zeros 16384)" --capture-header X-Origin-Requests --max-captured-values 1 \
    --min-cache-hits 1 --min-cache-misses 1 --agent-pid 1 --agent-metrics-url http://agent:9900/metrics
fi

if has_scenario cache-eviction; then
  eviction_duration="$duration"
  [[ "$profile" == "smoke" ]] && eviction_duration="30s"
  add_case "cache-eviction-16m-h1" agent-bench run --suite "$suite" --protocol h1 --scenario cache-eviction-16m-h1 \
    --url https://agent:8444/bytes/16777216 --host cache.benchmark.example.test --insecure-skip-verify \
    --concurrency 4 --warmup 1s --duration "$eviction_duration" --repeats 1 --unique-query \
    --expected-sha256 "$(sha256_zeros 16777216)" --min-cache-misses 1 --min-cache-evictions 1 \
    --agent-pid 1 --agent-metrics-url http://agent:9900/metrics
fi

if has_scenario range; then
  for protocol in $protocols; do
    common_case "range-64k-of-1m-${protocol}" "$protocol" benchmark.example.test /bytes/1048576 32 \
      --header Range=bytes=0-65535 --expected-status 206 --expected-header "Content-Range=bytes 0-65535/1048576" \
      --expected-sha256 "$(sha256_zeros 65536)"
  done
fi

if has_scenario large-transfer; then
	large_concurrency=8
	[[ "$profile" == "smoke" ]] && large_concurrency=2
	for protocol in $protocols; do
		common_case "large-transfer-16m-${protocol}" "$protocol" benchmark.example.test /bytes/16777216 "$large_concurrency" \
      --expected-sha256 "$(sha256_zeros 16777216)"
  done
fi

if has_scenario multi-domain; then
  for host in cache.benchmark.example.test cache-alt.benchmark.example.test; do
    common_case "multi-domain-${host%%.*}" h2 "$host" "/bytes/16384?domain=$host" 32 \
      --expected-sha256 "$(sha256_zeros 16384)" --expected-header X-Cache=HIT
  done
fi

if has_scenario multi-origin; then
  for protocol in $protocols; do
    common_case "multi-origin-${protocol}" "$protocol" multi.benchmark.example.test /bytes/16384 64 \
      --expected-sha256 "$(sha256_zeros 16384)" --capture-header X-Benchmark-Origin
  done
fi

if has_scenario origin-resilience; then
  common_case origin-slow-100ms-h2 h2 benchmark.example.test /delay/100/bytes/16384 32 \
    --expected-sha256 "$(sha256_zeros 16384)"
  common_case origin-failover-h2 h2 resilient.benchmark.example.test /failover/bytes/16384 32 \
    --expected-sha256 "$(sha256_zeros 16384)" --capture-header X-Benchmark-Origin
fi

if has_scenario bandwidth-limit; then
  common_case origin-throttle-8mib-h1 h1 benchmark.example.test /throttle/8388608/bytes/16777216 1 \
    --expected-sha256 "$(sha256_zeros 16777216)"
  add_case request-rate-limit-h1 agent-bench run --suite "$suite" --protocol h1 --scenario request-rate-limit-h1 \
    --url https://agent:8444/bytes/1024 --host limit.benchmark.example.test --insecure-skip-verify \
    --concurrency 8 --warmup 2s --duration "$duration" --repeats 1 --expected-status 429 \
    --agent-pid 1 --agent-metrics-url http://agent:9900/metrics
fi

echo "CDN benchmark profile=$profile runner=$runner cases=${#case_names[@]} run=$run_id"
if $dry_run; then
  for index in "${!case_names[@]}"; do printf '%2d  %s\n' "$((index+1))" "${case_names[index]}"; done
  exit 0
fi

command -v docker >/dev/null 2>&1 || die "docker is required"
if [[ "$runner" == 26c-* ]]; then
  docker_cpus="$(docker info --format '{{.NCPU}}' 2>/dev/null)"
  [[ "$docker_cpus" =~ ^[0-9]+$ ]] && ((docker_cpus >= 26)) || die "26c runner requires at least 26 Docker CPUs"
fi

result_dir="$RESULTS_ROOT/$run_id"
mkdir -p "$result_dir"
summary_file="$result_dir/cdn-matrix.tsv"
printf 'scenario\tprofile\tstatus\tresult_directory\n' > "$summary_file"

if ! $reuse_environment; then
  compose down --volumes --remove-orphans >/dev/null 2>&1 || true
  (cd "$REPO_ROOT" && go run ./cmd/agent-bench-pki --output "$STATE_DIR")
else
  [[ -s "$STATE_DIR/identity.json" && -s "$STATE_DIR/initial-tasks.json" ]] || \
    die "--reuse-environment requires state generated by the current benchmark PKI tool"
fi
compose --profile run build origin origin2 gateway agent load
compose up -d --no-build origin origin2 redis gateway agent
if $cleanup; then trap 'compose down --remove-orphans >/dev/null 2>&1 || true' EXIT; fi

ready=false
for _attempt in $(seq 1 60); do
  if compose exec -T agent agent-bench run --suite pr --protocol h1 --scenario readiness \
      --url https://127.0.0.1:8444/bytes/1 --host benchmark.example.test --insecure-skip-verify \
      --duration 100ms --warmup 1ms --repeats 1 --concurrency 1 --output /tmp/agent-bench-readiness >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 1
done
$ready || die "Edge Agent did not become ready"
compose exec -T agent edge-agent benchmark --directory /opt/goveto-edge/cache > "$result_dir/hardware.json" || die "hardware benchmark failed"

failures=0
for index in "${!case_names[@]}"; do
  name="${case_names[index]}"
  output="$result_dir/$name"
  mkdir -p "$output"
  IFS=$'\t' read -r -a args <<< "${case_args[index]}"
  args+=(--output "/results/$run_id/$name")
  echo "[$((index+1))/${#case_names[@]}] $name"
  if compose --profile run run --rm load "${args[@]}" 2>&1 | tee "$output/run.log"; then
    status=PASS
  else
    status=FAIL
    failures=$((failures+1))
  fi
  printf '%s\t%s\t%s\t%s\n' "$name" "$profile" "$status" "$name" >> "$summary_file"
done

echo "CDN benchmark complete: ${#case_names[@]} cases, $failures failed"
echo "Results: $result_dir"
((failures == 0))
