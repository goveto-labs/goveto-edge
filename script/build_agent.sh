#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
artifact_dir="$repo_root/static/agent"
build_dir="$(mktemp -d)"
publishing=0
version="${GOVETO_VERSION:-dev}"

is_canonical_semver() {
  local value="$1" core prerelease build identifier major minor patch version_without_build
  local -a prerelease_ids build_ids
  [[ "$value" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$ ]] || return 1
  core="${value#v}"
  core="${core%%[-+]*}"
  IFS='.' read -r major minor patch <<< "$core"
  for identifier in "$major" "$minor" "$patch"; do
    [[ "$identifier" == "0" || "$identifier" != 0* ]] || return 1
  done
  version_without_build="${value%%+*}"
  if [[ "$version_without_build" == *-* ]]; then
    prerelease="${version_without_build#*-}"
    IFS='.' read -ra prerelease_ids <<< "$prerelease"
    for identifier in "${prerelease_ids[@]}"; do
      [[ -n "$identifier" ]] || return 1
      if [[ "$identifier" =~ ^[0-9]+$ ]]; then
        [[ "$identifier" == "0" || "$identifier" != 0* ]] || return 1
      fi
    done
  fi
  if [[ "$value" == *+* ]]; then
    build="${value#*+}"
    IFS='.' read -ra build_ids <<< "$build"
    for identifier in "${build_ids[@]}"; do
      [[ -n "$identifier" ]] || return 1
    done
  fi
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version)
      [[ $# -ge 2 ]] || { echo "--version requires a value" >&2; exit 2; }
      version="$2"
      shift 2
      ;;
    *)
      echo "unknown argument: $1" >&2
      exit 2
      ;;
  esac
done

if [[ "$version" != "dev" ]] && ! is_canonical_semver "$version"; then
  echo "version must be dev or canonical SemVer beginning with v (for example v1.2.3)" >&2
  exit 2
fi

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
  rm -f "$artifact_dir/.manifest.json.new"
  rm -rf "$build_dir"
  return "$status"
}
trap cleanup EXIT
cd "$repo_root"

for arch in amd64 arm64; do
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build \
    -trimpath -ldflags="-s -w -X goveto-edge/internal/buildinfo.Version=$version" \
    -o "$build_dir/agent-linux-$arch" ./cmd/edge-agent
done

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

amd64_sha="$(sha256_file "$build_dir/agent-linux-amd64")"
arm64_sha="$(sha256_file "$build_dir/agent-linux-arm64")"
printf '{\n  "version": "%s",\n  "artifacts": {\n    "amd64": {"sha256": "%s"},\n    "arm64": {"sha256": "%s"}\n  }\n}\n' \
  "$version" "$amd64_sha" "$arm64_sha" > "$artifact_dir/.manifest.json.new"

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
mv -f "$artifact_dir/.manifest.json.new" "$artifact_dir/manifest.json"
publishing=0
