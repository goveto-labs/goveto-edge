#!/usr/bin/env bash

set -uo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
readonly COMPOSE_BASE="$REPO_ROOT/deploy/benchmark/compose.yaml"
readonly COMPOSE_26C="$REPO_ROOT/deploy/benchmark/compose.26c.yaml"
readonly COMPOSE_26C_AGENT2="$REPO_ROOT/deploy/benchmark/compose.26c-agent2.yaml"
readonly COMPOSE_26C_AGENT4="$REPO_ROOT/deploy/benchmark/compose.26c-agent4.yaml"
readonly COMPOSE_26C_AGENT4_LOAD10="$REPO_ROOT/deploy/benchmark/compose.26c-agent4-load10.yaml"
readonly COMPOSE_PROJECT="goveto-edge-benchmark"
readonly STATE_DIR="$REPO_ROOT/deploy/benchmark/state"
readonly RESULTS_ROOT="$REPO_ROOT/deploy/benchmark/results"
readonly MIN_UDP_BUFFER=7500000

mode="quick"
runner="default"
protocols="h1 h2 h3"
run_id="$(date -u +%Y%m%dT%H%M%SZ)"
max_load_cpu="85"
soak_protocols="h2"
soak_duration="6h"
reuse_environment=false
cleanup=false
dry_run=false
baseline_run=""
result_dir=""
summary_file=""
last_status=""

declare -A screen_status=()
declare -A capacity_status=()
declare -A status_counts=()

usage() {
  cat <<'EOF'
Usage:
  script/run_agent_benchmark.sh quick [options]
  script/run_agent_benchmark.sh full [options]

Modes:
  quick  Run every origin and CDN scenario with short timings. It omits only
         long Capacity repetitions and the soak test.
  full   Run the same complete screening suite, Capacity timings only for PASS
         cases, then the long 1 MiB cache-hit stability test.

Options:
  --runner <name>             default, 26c-agent2, 26c-agent4, 26c-agent4-load10, or 26c-agent8
  --protocols "h1 h2 h3"      Protocols to include (default: all)
  --run-id <name>             Result directory name
  --max-load-cpu <percent>    Load saturation threshold (default: 85)
  --soak-protocols "h2"       Full-mode stability protocols (default: h2)
  --soak-duration <duration>  Full-mode stability duration (default: 6h)
  --baseline-run <run-id>     Compare each case with the same case from this run
  --reuse-environment         Keep benchmark volumes and credentials
  --cleanup                   Stop containers after the run
  --dry-run                   Print all expanded cases without running Docker
  -h, --help                  Show this help
EOF
}

die() { echo "error: $*" >&2; exit 1; }

if (($# > 0)) && [[ "$1" == "quick" || "$1" == "full" ]]; then
  mode="$1"
  shift
fi

while (($# > 0)); do
  case "$1" in
    --mode) mode="${2:-}"; shift 2 ;;
    --runner) runner="${2:-}"; shift 2 ;;
    --protocols) protocols="${2:-}"; shift 2 ;;
    --run-id) run_id="${2:-}"; shift 2 ;;
    --max-load-cpu) max_load_cpu="${2:-}"; shift 2 ;;
    --soak-protocols) soak_protocols="${2:-}"; shift 2 ;;
    --soak-duration) soak_duration="${2:-}"; shift 2 ;;
    --baseline-run) baseline_run="${2:-}"; shift 2 ;;
    --reuse-environment) reuse_environment=true; shift ;;
    --cleanup) cleanup=true; shift ;;
    --dry-run) dry_run=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done

case "$mode" in quick|full) ;; *) die "mode must be quick or full" ;; esac
[[ "$runner" == "26c" ]] && runner="26c-agent8"
case "$runner" in default|26c-agent2|26c-agent4|26c-agent4-load10|26c-agent8) ;; *) die "invalid runner: $runner" ;; esac
[[ "$run_id" =~ ^[A-Za-z0-9._-]+$ ]] || die "invalid run-id"
[[ -z "$baseline_run" || "$baseline_run" =~ ^[A-Za-z0-9._-]+$ ]] || die "invalid baseline run-id"
[[ "$max_load_cpu" =~ ^[0-9]+([.][0-9]+)?$ ]] || die "max-load-cpu must be numeric"
for protocol in $protocols $soak_protocols; do
  case "$protocol" in h1|h2|h3) ;; *) die "invalid protocol: $protocol" ;; esac
done

compose_args=(-p "$COMPOSE_PROJECT" -f "$COMPOSE_BASE")
case "$runner" in
  26c-agent2) compose_args+=(-f "$COMPOSE_26C_AGENT2") ;;
  26c-agent4) compose_args+=(-f "$COMPOSE_26C_AGENT4") ;;
  26c-agent4-load10) compose_args+=(-f "$COMPOSE_26C_AGENT4_LOAD10") ;;
  26c-agent8) compose_args+=(-f "$COMPOSE_26C") ;;
esac
compose() { docker compose "${compose_args[@]}" "$@"; }

has_protocol() { [[ " $1 " == *" $2 "* ]]; }

sha256_zeros() {
  local size="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    head -c "$size" /dev/zero | sha256sum | awk '{print $1}'
  else
    head -c "$size" /dev/zero | shasum -a 256 | awk '{print $1}'
  fi
}

wait_for_agent() {
  local ready=false
  for _attempt in $(seq 1 60); do
    if compose exec -T agent agent-bench run --suite pr --protocol h1 --scenario readiness \
        --url https://127.0.0.1:8444/bytes/1 --host benchmark.example.test \
        --insecure-skip-verify --duration 100ms --warmup 1ms --repeats 1 \
        --concurrency 1 --output /tmp/agent-bench-readiness >/dev/null 2>&1; then
      ready=true
      break
    fi
    sleep 1
  done
  $ready
}

h3_buffer_warning() {
  grep -Eqi 'failed to sufficiently increase.*buffer|receive buffer size|send buffer size' "$1"
}

capture_environment() {
  local commit dirty rmem_max wmem_max
  commit="$(git -C "$REPO_ROOT" rev-parse HEAD 2>/dev/null || true)"
  dirty=false
  [[ -z "$(git -C "$REPO_ROOT" status --porcelain --untracked-files=normal 2>/dev/null)" ]] || dirty=true
  rmem_max="$(compose exec -T agent sh -c 'cat /proc/sys/net/core/rmem_max' 2>/dev/null || true)"
  wmem_max="$(compose exec -T agent sh -c 'cat /proc/sys/net/core/wmem_max' 2>/dev/null || true)"

  compose config > "$result_dir/compose.resolved.yaml"
  compose images > "$result_dir/images.txt"
  compose logs --no-color agent > "$result_dir/agent-startup.log" 2>&1
  jq -n --arg commit "$commit" --argjson dirty "$dirty" --arg mode "$mode" \
    --arg runner "$runner" --arg protocols "$protocols" --arg rmem_max "$rmem_max" \
    --arg wmem_max "$wmem_max" --arg max_load_cpu "$max_load_cpu" \
    --arg soak_protocols "$soak_protocols" --arg soak_duration "$soak_duration" \
    '{commit: $commit, dirty: $dirty, mode: $mode, runner: $runner, protocols: ($protocols | split(" ")), max_load_cpu_percent: ($max_load_cpu | tonumber), udp: {rmem_max: (try ($rmem_max | tonumber) catch null), wmem_max: (try ($wmem_max | tonumber) catch null), required_bytes: 7500000}, soak: {protocols: ($soak_protocols | split(" ")), duration: $soak_duration}}' \
    > "$result_dir/environment.json"

  if has_protocol "$protocols $soak_protocols" h3; then
    [[ "$rmem_max" =~ ^[0-9]+$ && "$wmem_max" =~ ^[0-9]+$ ]] || \
      die "cannot read UDP buffer limits from the Agent network namespace"
    ((rmem_max >= MIN_UDP_BUFFER && wmem_max >= MIN_UDP_BUFFER)) || \
      die "H3 requires UDP buffers >= $MIN_UDP_BUFFER bytes (got $rmem_max/$wmem_max)"
    h3_buffer_warning "$result_dir/agent-startup.log" && \
      die "quic-go reported an insufficient UDP buffer; inspect agent-startup.log"
  fi
}

capture_case_logs() {
  local output="$1" since="$2"
  compose logs --no-color --since "$since" agent > "$output/agent.log" 2>&1 || true
  compose logs --no-color --since "$since" origin > "$output/origin.log" 2>&1 || true
  compose logs --no-color --since "$since" origin2 > "$output/origin2.log" 2>&1 || true
  compose logs --no-color --since "$since" gateway > "$output/gateway.log" 2>&1 || true
}

report_status() {
  local report="$1"
  if [[ ! -s "$report" ]]; then
    echo PRODUCT_FAIL
    return
  fi
  jq -r 'if .baseline and (.baseline.passed == false) then "PRODUCT_FAIL" elif .validity.status then .validity.status elif .validity.valid then "PASS" else "ENV_INVALID" end' "$report"
}

record_skip() {
  local phase="$1" name="$2" protocol="$3" reason="$4"
  if $dry_run; then
    printf '%-10s %-42s protocol=%s (%s)\n' "$phase" "$name" "$protocol" "$reason"
  else
    printf '%s\t%s\t%s\tSKIPPED_NOT_PASS\t%s\t%s\n' "$phase" "$name" "$protocol" "$reason" "-" >> "$summary_file"
  fi
}

run_case() {
  local phase="$1" key="$2" name="$3" protocol="$4"
  shift 4
  local output container_output started status baseline_report container_baseline
  local -a baseline_args=()

  if $dry_run; then
    printf '%-10s %-42s protocol=%s\n' "$phase" "$name" "$protocol"
    last_status=PASS
    status_counts[PLANNED]=$(( ${status_counts[PLANNED]:-0} + 1 ))
    if [[ "$phase" == "screen" ]]; then
      screen_status["$key"]=PASS
    elif [[ "$phase" == "capacity" ]]; then
      capacity_status["$key"]=PASS
    fi
    return
  fi

  output="$result_dir/$phase/$name"
  container_output="/results/$run_id/$phase/$name"
  if [[ -n "$baseline_run" ]]; then
    baseline_report="$RESULTS_ROOT/$baseline_run/$phase/$name/report.json"
    container_baseline="/results/$baseline_run/$phase/$name/report.json"
    [[ -s "$baseline_report" ]] || die "baseline case is missing: $baseline_report"
    baseline_args=(--baseline "$container_baseline")
  fi
  mkdir -p "$output"
  started="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "[$phase] $name"
  if compose --profile run run --rm load agent-bench run \
      --agent-pid 1 --agent-metrics-url http://agent:9900/metrics \
      --agent-binary /usr/local/bin/edge-agent --max-load-cpu "$max_load_cpu" \
      --runner-id "$runner" --output "$container_output" "${baseline_args[@]}" "$@" 2>&1 | tee "$output/run.log"; then
    :
  else
    : # Read the typed result from report.json below.
  fi
  status="$(report_status "$output/report.json")"
  if [[ "$status" == "ENV_INVALID" ]] && jq -e '
    .validity.status == "ENV_INVALID" and
    (.validity.reasons | length) == 1 and
    (.validity.reasons[0] | startswith("RPS coefficient of variation "))
  ' "$output/report.json" >/dev/null; then
    cp "$output/report.json" "$output/report.cv-invalid-attempt-1.json"
    echo "[$phase] $name: retrying once after isolated RPS CV invalidation"
    if compose --profile run run --rm load agent-bench run \
        --agent-pid 1 --agent-metrics-url http://agent:9900/metrics \
        --agent-binary /usr/local/bin/edge-agent --max-load-cpu "$max_load_cpu" \
        --runner-id "$runner" --output "$container_output" "${baseline_args[@]}" "$@" 2>&1 | tee "$output/run.log"; then
      :
    else
      :
    fi
    status="$(report_status "$output/report.json")"
  fi
  capture_case_logs "$output" "$started"
  if [[ "$protocol" == "h3" ]] && h3_buffer_warning "$output/agent.log"; then
    status=ENV_INVALID
  fi
  last_status="$status"
  status_counts["$status"]=$(( ${status_counts["$status"]:-0} + 1 ))
  printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$phase" "$name" "$protocol" "$status" "" "$phase/$name" >> "$summary_file"
  if [[ "$phase" == "screen" ]]; then
    screen_status["$key"]="$status"
  elif [[ "$phase" == "capacity" ]]; then
    capacity_status["$key"]="$status"
  fi
}

run_origin_screen() {
  local protocol size mode concurrency name key
  for protocol in $protocols; do
    for size in 1024 16384 1048576; do
      for mode in reuse new; do
        for concurrency in 1 8 32 128; do
          name="pure-origin-${size}b-${mode}-${protocol}-c${concurrency}"
          key="origin:$name"
          args=(--suite pr --protocol "$protocol" --scenario "pure-origin-${size}b-${mode}" \
            --url "https://agent:8444/bytes/$size" --host benchmark.example.test --insecure-skip-verify \
            --concurrency "$concurrency" --warmup 1s --duration 5s --repeats 1 \
            --expected-sha256 "$(sha256_zeros "$size")")
          [[ "$mode" == "new" ]] && args+=(--new-connection)
          if [[ "$protocol" == "h3" && "$mode" == "new" && ( "$concurrency" == "32" || "$concurrency" == "128" ) ]]; then
            args+=(--cooldown 60s)
          fi
          run_case screen "$key" "$name" "$protocol" "${args[@]}"
        done
        if [[ "$mode" == "new" ]]; then
          name="pure-origin-${size}b-new-${protocol}-c512"
          key="origin:$name"
          local gate="origin:pure-origin-${size}b-new-${protocol}-c128"
          if [[ "${screen_status[$gate]:-}" != "PASS" ]]; then
            record_skip screen "$name" "$protocol" "c128=${screen_status[$gate]:-missing}"
            continue
          fi
          run_case screen "$key" "$name" "$protocol" \
            --suite pr --protocol "$protocol" --scenario "pure-origin-${size}b-new" \
            --url "https://agent:8444/bytes/$size" --host benchmark.example.test --insecure-skip-verify \
            --concurrency 512 --warmup 1s --duration 5s --repeats 1 --new-connection \
            --capacity-probe --cooldown 60s --expected-sha256 "$(sha256_zeros "$size")"
        fi
      done
    done
  done
}

run_origin_capacity() {
  local protocol size concurrency name key
  for protocol in $protocols; do
    for size in 1024 16384 1048576; do
      for concurrency in 32 128; do
        name="pure-origin-${size}b-reuse-${protocol}-c${concurrency}"
        key="origin:$name"
        if [[ "${screen_status[$key]:-}" != "PASS" ]]; then
          record_skip capacity "$name" "$protocol" "screen=${screen_status[$key]:-missing}"
          continue
        fi
        local capacity_args=()
        [[ "$concurrency" == "128" ]] && capacity_args+=(--cooldown 60s)
        run_case capacity "$key" "$name" "$protocol" \
          --suite capacity --protocol "$protocol" --scenario "pure-origin-${size}b-reuse" \
          --url "https://agent:8444/bytes/$size" --host benchmark.example.test --insecure-skip-verify \
          --concurrency "$concurrency" --warmup 30s --duration 120s --repeats 3 \
          --expected-sha256 "$(sha256_zeros "$size")" "${capacity_args[@]}"
      done
    done
  done
}

run_cdn_case() {
  local phase="$1" name="$2" protocol="$3"
  shift 3
  local key="cdn:$name"
  if [[ "$phase" == "capacity" && "${screen_status[$key]:-}" != "PASS" ]]; then
    record_skip capacity "$name" "$protocol" "screen=${screen_status[$key]:-missing}"
    return
  fi
  run_case "$phase" "$key" "$name" "$protocol" "$@"
}

purge_eviction_keys() {
  $dry_run && return
  local key
  for key in 1 2; do
    compose --profile run run --rm load agent-bench run \
      --suite pr --protocol h1 --scenario eviction-purge \
      --method PURGE --url "https://agent:8444/pattern/16777216?_bench=$key" \
      --host cache.benchmark.example.test --insecure-skip-verify \
      --concurrency 1 --skip-warmup --duration 100ms --repeats 1 --expected-status 204 \
      --output "/tmp/agent-bench-eviction-purge-$key" >/dev/null || \
      die "failed to purge eviction benchmark key $key"
  done
}

run_cdn_suite() {
  local phase="$1" suite="$2" warmup="$3" duration="$4" repeats="$5"
  local protocol size name
  for protocol in $protocols; do
    for size in 1024 16384 1048576; do
      name="cache-hit-${size}b-${protocol}"
      run_cdn_case "$phase" "$name" "$protocol" --suite "$suite" --protocol "$protocol" --scenario "$name" \
        --url "https://agent:8444/bytes/$size?asset=hit-$size" --host cache.benchmark.example.test \
        --insecure-skip-verify --concurrency 32 --warmup "$warmup" --duration "$duration" --repeats "$repeats" \
        --expected-sha256 "$(sha256_zeros "$size")" \
        --allowed-header X-Cache=HIT --allowed-header X-Cache=STALE --max-header-ratio X-Cache=STALE:0.01 \
        --capture-header X-Cache --min-cache-hits 1
    done
  done

  for protocol in $protocols; do
    name="cache-miss-16k-${protocol}"
    run_cdn_case "$phase" "$name" "$protocol" --suite "$suite" --protocol "$protocol" --scenario "$name" \
      --url https://agent:8444/bytes/16384 --host cache.benchmark.example.test --insecure-skip-verify \
      --concurrency 32 --warmup "$warmup" --duration "$duration" --repeats "$repeats" --unique-query \
      --expected-sha256 "$(sha256_zeros 16384)" --expected-header X-Cache=MISS \
      --capture-header X-Cache --min-cache-misses 1
  done

  if has_protocol "$protocols" h2; then
    run_cdn_case "$phase" cache-coalescing-h2 h2 --suite "$suite" --protocol h2 --scenario cache-coalescing-h2 \
      --url "https://agent:8444/delay/100/bytes/16384?asset=coalesce-$run_id-$phase" \
      --host cache.benchmark.example.test --insecure-skip-verify --concurrency 128 --skip-warmup \
      --duration "$duration" --repeats 1 --expected-sha256 "$(sha256_zeros 16384)" \
      --capture-header X-Origin-Requests --max-captured-values 1 --min-cache-hits 1 --min-cache-misses 1
  fi

  local eviction_duration="$duration"
  [[ "$phase" == "screen" ]] && eviction_duration="30s"
  if has_protocol "$protocols" h1; then
    purge_eviction_keys
    run_cdn_case "$phase" cache-eviction-16m-h1 h1 --suite "$suite" --protocol h1 --scenario cache-eviction-16m-h1 \
      --url https://agent:8444/pattern/16777216 --host cache.benchmark.example.test --insecure-skip-verify \
      --concurrency 4 --warmup 1s --duration "$eviction_duration" --repeats 1 --unique-query --unique-query-cardinality 2 \
      --min-cache-misses 1 --min-cache-evictions 1 --max-agent-rss 536870912 --cooldown 60s
  fi

  for protocol in $protocols; do
    name="range-64k-of-1m-${protocol}"
    run_cdn_case "$phase" "$name" "$protocol" --suite "$suite" --protocol "$protocol" --scenario "$name" \
      --url https://agent:8444/bytes/1048576 --host benchmark.example.test --insecure-skip-verify \
      --concurrency 32 --warmup "$warmup" --duration "$duration" --repeats "$repeats" \
      --header Range=bytes=0-65535 --expected-status 206 \
      --expected-header "Content-Range=bytes 0-65535/1048576" --expected-sha256 "$(sha256_zeros 65536)"
  done

  local large_concurrency=8
  [[ "$phase" == "screen" ]] && large_concurrency=2
  for protocol in $protocols; do
    name="large-transfer-16m-${protocol}"
    run_cdn_case "$phase" "$name" "$protocol" --suite "$suite" --protocol "$protocol" --scenario "$name" \
      --url https://agent:8444/bytes/16777216 --host benchmark.example.test --insecure-skip-verify \
      --concurrency "$large_concurrency" --warmup "$warmup" --duration "$duration" --repeats "$repeats" \
      --expected-sha256 "$(sha256_zeros 16777216)"
  done

  if has_protocol "$protocols" h2; then
    run_cdn_case "$phase" multi-domain-cache h2 --suite "$suite" --protocol h2 --scenario multi-domain-cache \
      --url "https://agent:8444/bytes/16384?domain=cache.benchmark.example.test" \
      --host cache.benchmark.example.test --insecure-skip-verify --concurrency 32 \
      --warmup "$warmup" --duration "$duration" --repeats "$repeats" \
      --expected-sha256 "$(sha256_zeros 16384)" \
      --allowed-header X-Cache=HIT --allowed-header X-Cache=STALE --max-header-ratio X-Cache=STALE:0.01
    run_cdn_case "$phase" multi-domain-cache-alt h2 --suite "$suite" --protocol h2 --scenario multi-domain-cache-alt \
      --url "https://agent:8444/bytes/16384?domain=cache-alt.benchmark.example.test" \
      --host cache-alt.benchmark.example.test --insecure-skip-verify --concurrency 32 \
      --warmup "$warmup" --duration "$duration" --repeats "$repeats" \
      --expected-sha256 "$(sha256_zeros 16384)" \
      --allowed-header X-Cache=HIT --allowed-header X-Cache=STALE --max-header-ratio X-Cache=STALE:0.01
  fi

  for protocol in $protocols; do
    name="multi-origin-${protocol}"
    run_cdn_case "$phase" "$name" "$protocol" --suite "$suite" --protocol "$protocol" --scenario "$name" \
      --url https://agent:8444/bytes/16384 --host multi.benchmark.example.test --insecure-skip-verify \
      --concurrency 64 --warmup "$warmup" --duration "$duration" --repeats "$repeats" \
      --expected-sha256 "$(sha256_zeros 16384)" --capture-header X-Benchmark-Origin
  done

  if has_protocol "$protocols" h2; then
    run_cdn_case "$phase" origin-slow-100ms-h2 h2 --suite "$suite" --protocol h2 --scenario origin-slow-100ms-h2 \
      --url https://agent:8444/delay/100/bytes/16384 --host benchmark.example.test --insecure-skip-verify \
      --concurrency 32 --warmup "$warmup" --duration "$duration" --repeats "$repeats" \
      --expected-sha256 "$(sha256_zeros 16384)"
    run_cdn_case "$phase" origin-failover-h2 h2 --suite "$suite" --protocol h2 --scenario origin-failover-h2 \
      --url https://agent:8444/failover/bytes/16384 --host resilient.benchmark.example.test --insecure-skip-verify \
      --concurrency 32 --warmup "$warmup" --duration "$duration" --repeats "$repeats" \
      --expected-sha256 "$(sha256_zeros 16384)" --expected-header X-Benchmark-Origin=origin-2 --capture-header X-Benchmark-Origin
  fi
  if has_protocol "$protocols" h1; then
    run_cdn_case "$phase" origin-throttle-8mib-h1 h1 --suite "$suite" --protocol h1 --scenario origin-throttle-8mib-h1 \
      --url https://agent:8444/throttle/8388608/bytes/16777216 --host benchmark.example.test --insecure-skip-verify \
      --concurrency 1 --warmup "$warmup" --duration "$duration" --repeats "$repeats" \
      --expected-sha256 "$(sha256_zeros 16777216)"
    if [[ "$phase" == "screen" ]]; then
      run_cdn_case "$phase" request-rate-limit-h1 h1 --suite "$suite" --protocol h1 --scenario request-rate-limit-h1 \
        --url https://agent:8444/bytes/1024 --host limit.benchmark.example.test --insecure-skip-verify \
        --concurrency 8 --warmup "$warmup" --duration "$duration" --repeats 1 --expected-status 429
    else
      run_cdn_case "$phase" request-rate-limit-h1 h1 --suite "$suite" --protocol h1 --scenario request-rate-limit-h1 \
        --url https://agent:8444/bytes/1024 --host limit.benchmark.example.test --insecure-skip-verify \
        --concurrency 8 --skip-warmup --duration "$duration" --repeats 1 \
        --allowed-status 200 --allowed-status 429 --min-status-count 429=1 --max-status-count 200=200
    fi
  fi
}

run_soak() {
  local protocol name key
  for protocol in $soak_protocols; do
    has_protocol "$protocols" "$protocol" || { record_skip soak "cache-hit-1048576b-$protocol" "$protocol" "protocol not selected"; continue; }
    name="cache-hit-1048576b-${protocol}"
    key="cdn:$name"
    if [[ "${capacity_status[$key]:-}" != "PASS" ]]; then
      record_skip soak "$name" "$protocol" "capacity=${capacity_status[$key]:-missing}"
      continue
    fi
    run_case soak-preflight "$key" "$name-preflight" "$protocol" --suite soak --protocol "$protocol" --scenario "$name-soak-preflight" \
      --url "https://agent:8444/bytes/1048576?asset=hit-1048576" --host cache.benchmark.example.test \
      --insecure-skip-verify --concurrency 32 --warmup 30s --duration 15m --repeats 1 \
      --expected-sha256 "$(sha256_zeros 1048576)" \
      --allowed-header X-Cache=HIT --allowed-header X-Cache=STALE --max-header-ratio X-Cache=STALE:0.01 \
      --capture-header X-Cache --min-cache-hits 1
    if [[ "$last_status" != "PASS" ]]; then
      record_skip soak "$name" "$protocol" "preflight=$last_status"
      continue
    fi
    run_case soak "$key" "$name" "$protocol" --suite soak --protocol "$protocol" --scenario "$name-soak" \
      --url "https://agent:8444/bytes/1048576?asset=hit-1048576" --host cache.benchmark.example.test \
      --insecure-skip-verify --concurrency 32 --warmup 30s --duration "$soak_duration" --repeats 1 \
      --expected-sha256 "$(sha256_zeros 1048576)" \
      --allowed-header X-Cache=HIT --allowed-header X-Cache=STALE --max-header-ratio X-Cache=STALE:0.01 \
      --capture-header X-Cache --min-cache-hits 1
  done
}

setup_environment() {
  command -v docker >/dev/null 2>&1 || die "docker is required"
  command -v go >/dev/null 2>&1 || die "go is required"
  command -v jq >/dev/null 2>&1 || die "jq is required"
  docker compose version >/dev/null 2>&1 || die "docker compose is required"
  docker_cpus="$(docker info --format '{{.NCPU}}' 2>/dev/null)"
  [[ "$docker_cpus" =~ ^[0-9]+$ ]] || die "cannot determine Docker CPU count"
  ((docker_cpus >= 6)) || die "benchmark requires at least 6 Docker CPUs"
  if [[ "$runner" == 26c-* ]]; then
    ((docker_cpus >= 26)) || die "$runner requires at least 26 Docker CPUs"
  fi

  result_dir="$RESULTS_ROOT/$run_id"
  summary_file="$result_dir/matrix.tsv"
  mkdir -p "$result_dir"
  printf 'phase\tscenario\tprotocol\tstatus\treason\tresult_directory\n' > "$summary_file"

  if ! $reuse_environment; then
    compose down --volumes --remove-orphans >/dev/null 2>&1 || true
    (cd "$REPO_ROOT" && go run ./cmd/agent-bench-pki --output "$STATE_DIR")
  else
    [[ -s "$STATE_DIR/identity.json" && -s "$STATE_DIR/initial-tasks.json" ]] || \
      die "--reuse-environment requires existing benchmark state"
  fi

  build_commit="$(git -C "$REPO_ROOT" rev-parse HEAD)"
  compose --profile run build --build-arg "VCS_REF=$build_commit" origin origin2 gateway agent load
  compose up -d --no-build origin origin2 redis gateway agent
  $cleanup && trap 'compose down --remove-orphans >/dev/null 2>&1 || true' EXIT
  wait_for_agent || die "Edge Agent did not become ready"
  capture_environment
  compose exec -T agent edge-agent benchmark --directory /opt/goveto-edge/cache > "$result_dir/hardware.json" || \
    die "hardware benchmark failed"
}

print_summary() {
  echo "Benchmark complete: mode=$mode runner=$runner"
  if $dry_run; then
    echo "  PLANNED=${status_counts[PLANNED]:-0}"
    return
  fi
  for status in PASS PRODUCT_FAIL LOAD_SATURATED TARGET_SATURATED ENV_INVALID; do
    echo "  $status=${status_counts[$status]:-0}"
  done
  $dry_run || echo "Results: $result_dir"
}

if ! $dry_run; then
  setup_environment
fi

echo "Running complete quick screening suite..."
run_origin_screen
run_cdn_suite screen pr 2s 8s 1

if [[ "$mode" == "full" ]]; then
  echo "Starting Capacity stage for PASS cases..."
  $dry_run || { compose restart agent >/dev/null; wait_for_agent || die "Edge Agent did not recover before Capacity"; }
  run_origin_capacity
  run_cdn_suite capacity capacity 30s 120s 3
  echo "Starting long stability stage..."
  run_soak
fi

print_summary
(( ${status_counts[PRODUCT_FAIL]:-0} == 0 && ${status_counts[LOAD_SATURATED]:-0} == 0 && ${status_counts[ENV_INVALID]:-0} == 0 ))
