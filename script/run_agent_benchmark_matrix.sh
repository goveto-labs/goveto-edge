#!/usr/bin/env bash

set -uo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
readonly COMPOSE_FILE="$REPO_ROOT/deploy/benchmark/compose.yaml"
readonly COMPOSE_PROJECT="goveto-edge-benchmark"
readonly STATE_DIR="$REPO_ROOT/deploy/benchmark/state"
readonly RESULTS_ROOT="$REPO_ROOT/deploy/benchmark/results"

suite="capacity"
protocols="h1 h2 h3"
concurrencies="1 8 32 128 512"
sizes="16384"
connection_modes="reuse"
run_id="$(date -u +%Y%m%dT%H%M%SZ)"
reuse_environment=false
cleanup=false
dry_run=false

usage() {
  cat <<'EOF'
Usage: script/run_agent_benchmark_matrix.sh [options]

Options:
  --suite <pr|nightly|capacity|soak>  Benchmark suite (default: capacity)
  --protocols "h1 h2 h3"             Space-separated protocols
  --concurrencies "1 8 32 128 512"   Space-separated concurrency levels
  --sizes "1024 16384 1048576"       Space-separated origin payload sizes
  --connection-modes "reuse new"     Reused or new connection modes
  --run-id <name>                     Result directory name
  --full-origin                       Use all payload sizes and both connection modes
  --reuse-environment                 Keep existing benchmark volumes and credentials
  --cleanup                           Stop containers after the matrix finishes
  --dry-run                           Print the matrix without running Docker
  -h, --help                          Show this help

The default capacity matrix contains 15 cases and takes about 3 hours 8 minutes.
--full-origin contains 90 cases and takes about 18 hours 45 minutes.
EOF
}

die() {
  echo "error: $*" >&2
  exit 1
}

while (($# > 0)); do
  case "$1" in
    --suite) suite="${2:-}"; shift 2 ;;
    --protocols) protocols="${2:-}"; shift 2 ;;
    --concurrencies) concurrencies="${2:-}"; shift 2 ;;
    --sizes) sizes="${2:-}"; shift 2 ;;
    --connection-modes) connection_modes="${2:-}"; shift 2 ;;
    --run-id) run_id="${2:-}"; shift 2 ;;
    --full-origin) sizes="1024 16384 1048576"; connection_modes="reuse new"; shift ;;
    --reuse-environment) reuse_environment=true; shift ;;
    --cleanup) cleanup=true; shift ;;
    --dry-run) dry_run=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done

case "$suite" in pr|nightly|capacity|soak) ;; *) die "invalid suite: $suite" ;; esac
[[ "$run_id" =~ ^[A-Za-z0-9._-]+$ ]] || die "run-id may contain only letters, numbers, dot, underscore, and hyphen"

for protocol in $protocols; do
  case "$protocol" in h1|h2|h3) ;; *) die "invalid protocol: $protocol" ;; esac
done
for concurrency in $concurrencies; do
  [[ "$concurrency" =~ ^[1-9][0-9]*$ ]] || die "invalid concurrency: $concurrency"
done
for size in $sizes; do
  [[ "$size" =~ ^[1-9][0-9]*$ ]] || die "invalid payload size: $size"
  ((size <= 16777216)) || die "payload size exceeds origin limit: $size"
done
for mode in $connection_modes; do
  case "$mode" in reuse|new) ;; *) die "invalid connection mode: $mode" ;; esac
done

compose() {
  docker compose -p "$COMPOSE_PROJECT" -f "$COMPOSE_FILE" "$@"
}

sha256_zeros() {
  local size="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    head -c "$size" /dev/zero | sha256sum | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    head -c "$size" /dev/zero | shasum -a 256 | awk '{print $1}'
  else
    die "sha256sum or shasum is required"
  fi
}

case_count=0
for _protocol in $protocols; do
  for _size in $sizes; do
    for _mode in $connection_modes; do
      for _concurrency in $concurrencies; do case_count=$((case_count + 1)); done
    done
  done
done

echo "Edge Agent benchmark matrix"
echo "  suite: $suite"
echo "  cases: $case_count"
echo "  run:   $run_id"

if $dry_run; then
  for protocol in $protocols; do
    for size in $sizes; do
      for mode in $connection_modes; do
        for concurrency in $concurrencies; do
          echo "$protocol payload=$size connection=$mode concurrency=$concurrency"
        done
      done
    done
  done
  exit 0
fi

command -v docker >/dev/null 2>&1 || die "docker is required"
docker compose version >/dev/null 2>&1 || die "docker compose is required"
command -v go >/dev/null 2>&1 || die "go is required to generate benchmark PKI"
docker_cpus="$(docker info --format '{{.NCPU}}' 2>/dev/null)"
[[ "$docker_cpus" =~ ^[0-9]+$ ]] || die "cannot determine Docker CPU count"
((docker_cpus >= 6)) || die "benchmark cpuset requires at least 6 CPUs; Docker reports $docker_cpus"

result_dir="$RESULTS_ROOT/$run_id"
mkdir -p "$result_dir"
summary_file="$result_dir/matrix.tsv"
printf 'scenario\tprotocol\tpayload_bytes\tconnection\tconcurrency\tstatus\tresult_directory\n' > "$summary_file"

if ! $reuse_environment; then
  echo "Resetting benchmark-only containers and volumes..."
  compose down --volumes --remove-orphans >/dev/null 2>&1 || true
  (cd "$REPO_ROOT" && go run ./cmd/agent-bench-pki --output "$STATE_DIR")
else
  [[ -s "$STATE_DIR/identity.json" && -s "$STATE_DIR/initial-task.json" ]] || \
    die "--reuse-environment requires existing state; run once without it"
fi

echo "Building and starting benchmark services..."
compose up -d --build origin redis gateway agent

if $cleanup; then
  trap 'compose down --remove-orphans >/dev/null 2>&1 || true' EXIT
fi

echo "Waiting for the Edge Agent site configuration..."
ready=false
for _attempt in $(seq 1 60); do
  if compose exec -T agent agent-bench run \
      --suite pr --protocol h1 --scenario readiness \
      --url https://127.0.0.1:8444/bytes/1 --host benchmark.example.test \
      --insecure-skip-verify --duration 100ms --warmup 1ms --repeats 1 \
      --concurrency 1 --output /tmp/agent-bench-readiness >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 1
done
$ready || die "Edge Agent did not become ready within 60 seconds; inspect: docker compose -p $COMPOSE_PROJECT -f $COMPOSE_FILE logs"

compose exec -T agent edge-agent benchmark --directory /opt/goveto-edge/cache \
  > "$result_dir/hardware.json" || die "hardware benchmark failed"

failures=0
completed=0
for protocol in $protocols; do
  target="https://agent:8444"
  for size in $sizes; do
    body_sha256="$(sha256_zeros "$size")"
    for mode in $connection_modes; do
      for concurrency in $concurrencies; do
        scenario="pure-origin-${size}b-${mode}"
        case_name="${scenario}-${protocol}-c${concurrency}"
        container_output="/results/$run_id/$case_name"
        host_output="$result_dir/$case_name"
        mkdir -p "$host_output"
        args=(
          agent-bench run
          --suite "$suite"
          --protocol "$protocol"
          --scenario "$scenario"
          --url "$target/bytes/$size"
          --host benchmark.example.test
          --insecure-skip-verify
          --concurrency "$concurrency"
          --expected-sha256 "$body_sha256"
          --agent-pid 1
          --agent-metrics-url http://agent:9900/metrics
          --output "$container_output"
        )
        if [[ "$mode" == "new" ]]; then args+=(--new-connection); fi

        echo "[$((completed + 1))/$case_count] $case_name"
        if compose --profile run run --rm load "${args[@]}" 2>&1 | tee "$host_output/run.log"; then
          status="PASS"
        else
          status="FAIL"
          failures=$((failures + 1))
        fi
        completed=$((completed + 1))
        printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
          "$scenario" "$protocol" "$size" "$mode" "$concurrency" "$status" "$case_name" >> "$summary_file"
      done
    done
  done
done

echo "Matrix complete: $completed cases, $failures failed"
echo "Results: $result_dir"
((failures == 0))
