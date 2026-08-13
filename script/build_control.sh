#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
output="${1:-$repo_root/bin/control-api}"
cd "$repo_root"

"$repo_root/script/build_agent.sh"
mkdir -p "$(dirname "$output")"
go build -trimpath -o "$output" ./cmd/control-api
