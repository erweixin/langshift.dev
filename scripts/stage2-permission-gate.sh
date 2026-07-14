#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${root}"
source_commit="${PERMISSION_REPORT_SOURCE_COMMIT:-$(git rev-parse HEAD)}"
[[ "${source_commit}" =~ ^[0-9a-f]{40}$ ]] || { echo "source commit must be a full SHA" >&2; exit 2; }
export GOTMPDIR="${GOTMPDIR:-${root}/.tmp/go-build}"
export GOCACHE="${GOCACHE:-${root}/.tmp/go-cache}"
export GOMODCACHE="${GOMODCACHE:-/private/tmp/lites-go-mod}"
mkdir -p "${GOTMPDIR}" "${GOCACHE}" "${GOMODCACHE}" gate-reports/stage-2

LITES_SOURCE_COMMIT="${source_commit}" \
LITES_PERMISSION_REPORT="${root}/gate-reports/stage-2/permission-matrix-report.json" \
  go test -race -count=1 ./internal/authorization
printf 'stage-2 permission matrix: roles=6 resources=7 actions=6 cross_tenant_allows=0 wrong_owner_private_allows=0 status=passed\n'
