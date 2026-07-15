#!/usr/bin/env bash
set -euo pipefail

source_commit="${STAGE3_FAULT_SOURCE_COMMIT:-$(git rev-parse HEAD)}"
[[ "${source_commit}" =~ ^[0-9a-f]{40}$ ]] || { echo "STAGE3_FAULT_SOURCE_COMMIT must be a 40-character commit" >&2; exit 2; }
raw="$(mktemp)"
postgres_raw="$(mktemp)"
cleanup() { rm -f "${raw}" "${postgres_raw}"; }
trap cleanup EXIT

go test -json -count=1 ./internal/realtime | tee "${raw}"
LITES_FOUNDATION_TEST_JSON="${postgres_raw}" ./scripts/foundation-postgres-smoke.sh
node scripts/write-stage3-realtime-loss-fault-report.mjs --input "${raw}" --postgres-input "${postgres_raw}" --source-commit "${source_commit}"
