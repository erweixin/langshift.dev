#!/usr/bin/env bash
set -euo pipefail

source_commit="${STAGE3_FAULT_SOURCE_COMMIT:-$(git rev-parse HEAD)}"
[[ "${source_commit}" =~ ^[0-9a-f]{40}$ ]] || { echo "STAGE3_FAULT_SOURCE_COMMIT must be a 40-character commit" >&2; exit 2; }
raw="$(mktemp)"
cleanup() { rm -f "${raw}"; }
trap cleanup EXIT

LITES_FOUNDATION_TEST_JSON="${raw}" ./scripts/foundation-postgres-smoke.sh
node scripts/write-stage3-approval-expiry-fault-report.mjs --input "${raw}" --source-commit "${source_commit}"
