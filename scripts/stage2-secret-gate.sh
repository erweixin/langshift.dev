#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${root}"
source_commit="${SECRET_REPORT_SOURCE_COMMIT:-$(git rev-parse HEAD)}"
[[ "${source_commit}" =~ ^[0-9a-f]{40}$ ]] || { echo "source commit must be a full SHA" >&2; exit 2; }
runtime="$(mktemp -d "${TMPDIR:-/tmp}/lites-secret-gate.XXXXXX")"
trap 'rm -rf "${runtime}"' EXIT

export GOTMPDIR="${GOTMPDIR:-${root}/.tmp/go-build}"
export GOCACHE="${GOCACHE:-${root}/.tmp/go-cache}"
export GOMODCACHE="${GOMODCACHE:-/private/tmp/lites-go-mod}"
mkdir -p "${GOTMPDIR}" "${GOCACHE}" "${GOMODCACHE}"

go test -race -json -count=1 -tags=integration ./internal/observability ./internal/security/opaque ./internal/identity/session > "${runtime}/runtime.jsonl"
LITES_FOUNDATION_TEST_JSON="${runtime}/integration.jsonl" ./scripts/foundation-postgres-smoke.sh >/dev/null

trivy_image="aquasec/trivy:0.72.0@sha256:cffe3f5161a47a6823fbd23d985795b3ed72a4c806da4c4df16266c02accdd6f"
docker run --rm --volume "${root}:/workspace:ro" --workdir /workspace "${trivy_image}" filesystem --scanners secret --exit-code 1 --format json --skip-dirs .git --skip-dirs gate-reports . > "${runtime}/trivy-secrets.json"

node scripts/write-stage2-secret-report.mjs --runtime "${runtime}/runtime.jsonl" --integration "${runtime}/integration.jsonl" --trivy "${runtime}/trivy-secrets.json" --source-commit "${source_commit}" --output gate-reports/stage-2/secret-scan-report.json
