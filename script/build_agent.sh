#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
artifact_dir="$repo_root/static/agent"
build_dir="$(mktemp -d)"
publishing=0

cleanup() {
  status=$?
  set +e
  if [[ $status -ne 0 && $publishing -eq 1 ]]; then
    for arch in amd64 arm64; do
      target="$artifact_dir/agent-linux-$arch"
      backup="$build_dir/previous-$arch"
      if [[ -f "$backup" ]]; then
        install -m 0755 "$backup" "$target"
      else
        rm -f "$target"
      fi
    done
  fi
  rm -f "$artifact_dir/.agent-linux-amd64.new" "$artifact_dir/.agent-linux-arm64.new"
  rm -rf "$build_dir"
  return "$status"
}
trap cleanup EXIT
cd "$repo_root"

for arch in amd64 arm64; do
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build \
    -trimpath -ldflags="-s -w" \
    -o "$build_dir/agent-linux-$arch" ./cmd/edge-agent
done

for arch in amd64 arm64; do
  target="$artifact_dir/agent-linux-$arch"
  if [[ -f "$target" ]]; then
    cp -p "$target" "$build_dir/previous-$arch"
  fi
  install -m 0755 "$build_dir/agent-linux-$arch" "$artifact_dir/.agent-linux-$arch.new"
done

publishing=1
mv -f "$artifact_dir/.agent-linux-amd64.new" "$artifact_dir/agent-linux-amd64"
mv -f "$artifact_dir/.agent-linux-arm64.new" "$artifact_dir/agent-linux-arm64"
publishing=0
