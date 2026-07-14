#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

source_commit="${1:-$(git rev-parse HEAD)}"
if [[ ! "${source_commit}" =~ ^[0-9a-f]{40}$ ]]; then
  echo "source commit must be a full 40-character Git SHA" >&2
  exit 2
fi

model_report="${TMPDIR:-/tmp}/lites-stage2-claim-model-${$}.json"
cleanup() {
  rm -f "${model_report}"
}
trap cleanup EXIT

export GOTMPDIR="${GOTMPDIR:-$PWD/.tmp/go-build}"
export GOCACHE="${GOCACHE:-$PWD/.tmp/go-cache}"
export GOMODCACHE="${GOMODCACHE:-/private/tmp/lites-go-mod}"

LITES_CLAIM_GATE_MODEL_REPORT="${model_report}" \
LITES_SOURCE_COMMIT="${source_commit}" \
  go test -race -count=1 ./internal/identity/anonymousclaim

./scripts/foundation-postgres-smoke.sh

node scripts/write-stage2-claim-report.mjs \
  --model "${model_report}" \
  --source-commit "${source_commit}"
