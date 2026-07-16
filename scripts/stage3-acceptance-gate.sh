#!/usr/bin/env bash
set -euo pipefail

source_commit="${STAGE3_GATE_SOURCE_COMMIT:-$(git rev-parse HEAD)}"
[[ "${source_commit}" =~ ^[0-9a-f]{40}$ ]] || { echo "STAGE3_GATE_SOURCE_COMMIT must be a 40-character commit" >&2; exit 2; }
node scripts/write-stage3-acceptance-report.mjs \
  --source-commit "${source_commit}" \
  --reports "${STAGE3_REPORTS_DIRECTORY:-gate-reports/stage-3}" \
  --workspace-root "${STAGE3_WORKSPACE_ROOT:-$(pwd)}" \
  --output "${STAGE3_ACCEPTANCE_REPORT:-gate-reports/stage-3/stage3-acceptance-report.json}"
