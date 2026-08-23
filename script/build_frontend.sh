#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
artifact_dir="$repo_root/static/web/dist"
build_dir="$(mktemp -d)"
publishing=0

cleanup() {
  status=$?
  set +e
  if [[ $status -ne 0 && $publishing -eq 1 ]]; then
    if [[ -d "$artifact_dir" ]]; then
      rm -rf "$artifact_dir"
      if [[ -d "$build_dir/previous" ]]; then
        mv "$build_dir/previous" "$artifact_dir"
      fi
    fi
  fi
  rm -rf "$build_dir"
  return "$status"
}
trap cleanup EXIT
cd "$repo_root"

if ! command -v pnpm >/dev/null 2>&1; then
  echo "pnpm is required to build the console (see frontend/README or use corepack)" >&2
  exit 1
fi

pnpm --dir frontend install --frozen-lockfile
pnpm --dir frontend build

if [[ ! -f "$repo_root/frontend/dist/index.html" ]]; then
  echo "frontend build did not produce frontend/dist/index.html" >&2
  exit 1
fi

# Copy into a staging directory first so a partially copied tree can never be
# observed by a concurrent go build, then swap into place atomically.
cp -R "$repo_root/frontend/dist" "$build_dir/dist"

if [[ -d "$artifact_dir" ]]; then
  mv "$artifact_dir" "$build_dir/previous"
fi
mkdir -p "$(dirname "$artifact_dir")"
publishing=1
mv "$build_dir/dist" "$artifact_dir"
touch "$artifact_dir/.gitkeep"
publishing=0

echo "console assets published to static/web/dist"
