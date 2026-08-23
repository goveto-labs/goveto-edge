#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
output="$repo_root/bin/control-api"
version="${GOVETO_VERSION:-dev}"
output_set=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version)
      [[ $# -ge 2 ]] || { echo "--version requires a value" >&2; exit 2; }
      version="$2"
      shift 2
      ;;
    --output)
      [[ $# -ge 2 ]] || { echo "--output requires a value" >&2; exit 2; }
      output="$2"
      output_set=1
      shift 2
      ;;
    --*)
      echo "unknown argument: $1" >&2
      exit 2
      ;;
    *)
      # Preserve the original positional output argument.
      [[ $output_set -eq 0 ]] || { echo "output path was specified more than once" >&2; exit 2; }
      output="$1"
      output_set=1
      shift
      ;;
  esac
done
cd "$repo_root"

"$repo_root/script/build_agent.sh" --version "$version"
"$repo_root/script/build_frontend.sh"
mkdir -p "$(dirname "$output")"
go build -tags agent_artifacts,console_artifacts -trimpath \
  -ldflags="-X goveto-edge/internal/buildinfo.Version=$version" -o "$output" ./cmd/control-api
