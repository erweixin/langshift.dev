#!/usr/bin/env bash
set -euo pipefail

input="${1:-${STAGE3_CAPACITY_RESULT:-}}"
[[ -n "${input}" && -f "${input}" ]] || { echo "provide a raw production capacity result as the first argument or STAGE3_CAPACITY_RESULT" >&2; exit 2; }
source_commit="${STAGE3_GATE_SOURCE_COMMIT:-$(git rev-parse HEAD)}"
[[ "${source_commit}" =~ ^[0-9a-f]{40}$ ]] || { echo "STAGE3_GATE_SOURCE_COMMIT must be a 40-character commit" >&2; exit 2; }
node scripts/write-stage3-capacity-report.mjs --input "${input}" --source-commit "${source_commit}"
