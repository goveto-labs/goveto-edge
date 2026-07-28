#!/bin/sh
set -eu

if [ -z "${MAXMIND_LICENSE_KEY:-}" ]; then
  echo "MAXMIND_LICENSE_KEY is required" >&2
  exit 1
fi

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_dir=$(dirname -- "$script_dir")
target="$repo_dir/GeoLite2-City.mmdb"
work_dir=$(mktemp -d "${TMPDIR:-/tmp}/goveto-geoip.XXXXXX")
trap 'rm -rf "$work_dir"' EXIT HUP INT TERM

archive="$work_dir/GeoLite2-City.tar.gz"
if [ -n "${MAXMIND_DOWNLOAD_URL:-}" ]; then
  curl --fail --location --silent --show-error "$MAXMIND_DOWNLOAD_URL" --output "$archive"
else
  curl --fail --location --silent --show-error --get \
    --data-urlencode "edition_id=GeoLite2-City" \
    --data-urlencode "license_key=$MAXMIND_LICENSE_KEY" \
    --data-urlencode "suffix=tar.gz" \
    "https://download.maxmind.com/app/geoip_download" --output "$archive"
fi

tar -xzf "$archive" -C "$work_dir"
source_file=$(find "$work_dir" -type f -name 'GeoLite2-City.mmdb' -print -quit)
if [ -z "$source_file" ]; then
  echo "download did not contain GeoLite2-City.mmdb" >&2
  exit 1
fi

pending="$repo_dir/GeoLite2-City.mmdb.pending.$$"
trap 'rm -rf "$work_dir"; rm -f "$pending"' EXIT HUP INT TERM
cp "$source_file" "$pending"
chmod 0644 "$pending"
mv -f "$pending" "$target"
echo "installed $target"
