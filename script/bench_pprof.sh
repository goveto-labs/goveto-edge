#!/usr/bin/env bash
# Symbolize benchmark pprof artifacts locally.
#
# Benchmark runs capture raw .pprof files from the agent's HTTP endpoints. This
# script turns every profile of a run into readable top lists.
#
# Usage:
#   script/bench_pprof.sh [run-id]     # default: newest run under results/
#   script/bench_pprof.sh 20260812T093842Z
#
# Output is written to <run>/pprof-notes/<profile>.txt next to the artifacts.

set -euo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly RESULTS_ROOT="${BENCH_RESULTS_ROOT:-$SCRIPT_DIR/../deploy/benchmark/results}"

die() { echo "error: $*" >&2; exit 1; }

command -v go >/dev/null 2>&1 || die "go is required to run 'go tool pprof'"
[[ -d "$RESULTS_ROOT" ]] || die "benchmark results directory not found: $RESULTS_ROOT"

run_id="${1:-}"
if [[ -z "$run_id" ]]; then
  for candidate in "$RESULTS_ROOT"/*; do
    [[ -d "$candidate" ]] || continue
    candidate_id="${candidate##*/}"
    if [[ "$candidate_id" =~ ^[0-9]{8}T[0-9]{6}Z$ ]] && [[ -z "$run_id" || "$candidate_id" > "$run_id" ]]; then
      run_id="$candidate_id"
    fi
  done
  [[ -n "$run_id" ]] || die "no timestamped benchmark runs under $RESULTS_ROOT"
fi
run_dir="$RESULTS_ROOT/$run_id"
[[ -d "$run_dir" ]] || die "run directory not found: $run_dir"

notes_dir="$run_dir/pprof-notes"
first_profile="$(find "$run_dir" -type f -name '*.pprof' -print -quit)"
[[ -n "$first_profile" ]] || die "no .pprof files under $run_dir"
mkdir -p "$notes_dir"

symbolize_profile() {
  local profile="$1" out="$2" name="$3" flat cumulative application_all application
  if ! flat="$(go tool pprof -top -nodecount=15 "$profile" 2>&1)"; then
    echo "error: flat profile failed: $profile" >&2
    echo "$flat" >&2
    return 1
  fi
  if ! cumulative="$(go tool pprof -top -cum -nodecount=25 "$profile" 2>&1)"; then
    echo "error: cumulative profile failed: $profile" >&2
    echo "$cumulative" >&2
    return 1
  fi
  if ! application_all="$(go tool pprof -top -cum -nodecount=200 "$profile" 2>&1)"; then
    echo "error: application profile failed: $profile" >&2
    echo "$application_all" >&2
    return 1
  fi
  application="$(printf '%s\n' "$application_all" | grep -E 'goveto|caddy|grpc|lz4|quic' || true)"
  {
    echo "# $name"
    echo
    echo "## flat top"
    printf '%s\n' "$flat"
    echo
    echo "## cumulative top"
    printf '%s\n' "$cumulative"
    echo
    echo "## application frames (goveto/caddy/grpc/lz4/quic)"
    printf '%s\n' "$application"
  } > "$out"
}

count=0
failed=0
while IFS= read -r -d '' profile; do
  name="${profile#"$run_dir"/}"
  name="${name//\//_}"
  name="${name%.pprof}"
  out="$notes_dir/$name.txt"
  count=$((count + 1))
  if symbolize_profile "$profile" "$out" "$name"; then
    echo "wrote $out"
  else
    rm -f "$out"
    failed=$((failed + 1))
  fi
done < <(find "$run_dir" -type f -name '*.pprof' -print0)

((failed == 0)) || die "failed to symbolize $failed of $count profiles from $run_id"
echo "symbolized $count profiles from $run_id"
