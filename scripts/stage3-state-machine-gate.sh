#!/usr/bin/env bash
set -euo pipefail

source_commit="${STAGE3_GATE_SOURCE_COMMIT:-$(git rev-parse HEAD)}"
[[ "${source_commit}" =~ ^[0-9a-f]{40}$ ]] || { echo "STAGE3_GATE_SOURCE_COMMIT must be a 40-character commit" >&2; exit 2; }
raw="$(mktemp)"
cleanup() { rm -f "${raw}"; }
trap cleanup EXIT

go test -json -count=1 ./internal/execution/statemachine | tee "${raw}"
node scripts/write-stage3-state-machine-report.mjs --input "${raw}" --source-commit "${source_commit}"
