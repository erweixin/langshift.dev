#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: $0 IMAGE" >&2
  exit 64
fi

image=$1
service=web-app
report_dir=${SUPPLY_CHAIN_REPORT_DIR:-gate-reports/stage-2/images}
mkdir -p "$report_dir"

user=$(docker image inspect --format '{{.Config.User}}' "$image")
entrypoint=$(docker image inspect --format '{{json .Config.Entrypoint}}' "$image")
cmd=$(docker image inspect --format '{{json .Config.Cmd}}' "$image")
title=$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.title"}}' "$image")
version=$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.version"}}' "$image")
revision=$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' "$image")
license=$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.licenses"}}' "$image")
size=$(docker image inspect --format '{{.Size}}' "$image")
image_id=$(docker image inspect --format '{{.Id}}' "$image")

[[ "$user" == "65532:65532" || "$user" == "65532" ]]
[[ "$entrypoint" == '["/nodejs/bin/node"]' ]]
[[ "$cmd" == '["apps/web/server.js"]' ]]
[[ "$title" == "Lites web app" ]]
[[ -n "$version" && "$version" != '<no value>' ]]
[[ -n "$revision" && "$revision" != '<no value>' ]]
[[ "$license" == "AGPL-3.0-only" ]]
[[ "$size" -le 268435456 ]]

container=$(docker create "$image")
archive=$(mktemp "${TMPDIR:-/tmp}/lites-web-image.XXXXXX.tar")
cleanup() {
  docker rm -f "$container" >/dev/null 2>&1 || true
  rm -f "$archive"
}
trap cleanup EXIT
docker export --output "$archive" "$container"
files=$(tar -tf "$archive")

for forbidden in bin/sh bin/bash usr/bin/sh usr/bin/bash busybox; do
  if grep -Fxq "$forbidden" <<<"$files"; then
    echo "web runtime image contains forbidden shell path: $forbidden" >&2
    exit 1
  fi
done
for required in nodejs/bin/node app/apps/web/server.js LICENSE; do
  grep -Fxq "$required" <<<"$files"
done

cat >"$report_dir/$service.json" <<EOF
{
  "schema_version": "1.0.0",
  "service": "$service",
  "image": "$image",
  "image_id": "$image_id",
  "runtime_user": "65532:65532",
  "entrypoint": $entrypoint,
  "command": $cmd,
  "shell_present": false,
  "license": "$license",
  "version": "$version",
  "revision": "$revision",
  "size_bytes": $size,
  "max_size_bytes": 268435456,
  "status": "passed"
}
EOF
