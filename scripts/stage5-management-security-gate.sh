#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."
source_commit="${STAGE5_GATE_SOURCE_COMMIT:-$(git rev-parse HEAD)}"
[[ "${source_commit}" =~ ^[0-9a-f]{40}$ ]] || { echo "STAGE5_GATE_SOURCE_COMMIT must be a 40-character commit" >&2; exit 2; }
database_report="$(mktemp)"
cleanup() { rm -f "${database_report}"; }
trap cleanup EXIT

export GOTMPDIR="${GOTMPDIR:-$PWD/.tmp/go-tmp-stage5}"
export GOCACHE="${GOCACHE:-$PWD/.tmp/go-cache-stage5}"

go test -count=1 ./internal/contracts/api ./cmd/api-gateway

LITES_FOUNDATION_TEST_PACKAGES='./internal/contracts/postgres' \
LITES_FOUNDATION_TEST_RUN='TestContractControlPlaneRequiresCurrentTwoPersonApprovalAndSynchronizesSeats|TestControlServiceCommitsIdempotencyEventsContractsAdjustmentsAndUsage' \
LITES_FOUNDATION_TEST_JSON="${database_report}" LITES_FOUNDATION_TEST_QUIET=true \
  ./scripts/foundation-postgres-smoke.sh

node scripts/write-stage5-management-security-report.mjs \
  --database "${database_report}" \
  --source-commit "${source_commit}"
