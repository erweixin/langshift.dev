#!/usr/bin/env bash
set -euo pipefail

source_commit="${STAGE3_EFFECT_SOURCE_COMMIT:-${STAGE3_FAULT_SOURCE_COMMIT:-$(git rev-parse HEAD)}}"
[[ "${source_commit}" =~ ^[0-9a-f]{40}$ ]] || { echo "STAGE3_EFFECT_SOURCE_COMMIT must be a 40-character commit" >&2; exit 2; }
raw="${STAGE3_EFFECT_RAW_TEST_JSON:-$(mktemp)}"
cleanup() {
  if [[ -z "${STAGE3_EFFECT_RAW_TEST_JSON:-}" ]]; then rm -f "${raw}"; fi
}
trap cleanup EXIT

LITES_FOUNDATION_TEST_JSON="${raw}" ./scripts/foundation-postgres-smoke.sh
node scripts/write-stage3-effect-ledger-report.mjs \
  --input "${raw}" \
  --source-commit "${source_commit}" \
  --output "${STAGE3_EFFECT_REPORT:-gate-reports/stage-3/effect-ledger-reconciliation-report.json}"
