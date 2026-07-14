#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${root}"
source_commit="${IDENTITY_REPORT_SOURCE_COMMIT:-$(git rev-parse HEAD)}"
[[ "${source_commit}" =~ ^[0-9a-f]{40}$ ]] || { echo "source commit must be a full SHA" >&2; exit 2; }
runtime="$(mktemp -d "${TMPDIR:-/tmp}/lites-identity-gate.XXXXXX")"
trap 'rm -rf "${runtime}"' EXIT

export GOTMPDIR="${GOTMPDIR:-${root}/.tmp/go-build}"
export GOCACHE="${GOCACHE:-${root}/.tmp/go-cache}"
export GOMODCACHE="${GOMODCACHE:-/private/tmp/lites-go-mod}"
mkdir -p "${GOTMPDIR}" "${GOCACHE}" "${GOMODCACHE}"

go test -race -json -count=1 ./internal/gateway ./internal/authorization ./internal/identity/api ./internal/identity/password ./internal/identity/session > "${runtime}/unit.jsonl"
LITES_FOUNDATION_TEST_JSON="${runtime}/integration.jsonl" ./scripts/foundation-postgres-smoke.sh >/dev/null
node scripts/write-stage2-identity-report.mjs --unit "${runtime}/unit.jsonl" --integration "${runtime}/integration.jsonl" --source-commit "${source_commit}" --output gate-reports/stage-2/identity-e2e-report.json
