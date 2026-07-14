#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${root}"
source_commit="${STAGE2_REPORT_SOURCE_COMMIT:-$(git rev-parse HEAD)}"
[[ "${source_commit}" =~ ^[0-9a-f]{40}$ ]] || { echo "source commit must be a full SHA" >&2; exit 2; }
git cat-file -e "${source_commit}^{commit}"

node scripts/write-stage2-acceptance-report.mjs \
  --source-commit "${source_commit}" \
  --reports "${STAGE2_REPORTS_DIRECTORY:-gate-reports/stage-2}" \
  --workspace-root "${root}" \
  --output "${STAGE2_ACCEPTANCE_REPORT:-gate-reports/stage-2/stage2-acceptance-report.json}"
