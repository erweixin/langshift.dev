#!/usr/bin/env bash
set -euo pipefail

source_commit="${STAGE3_FAULT_SOURCE_COMMIT:-$(git rev-parse HEAD)}"
[[ "${source_commit}" =~ ^[0-9a-f]{40}$ ]] || { echo "STAGE3_FAULT_SOURCE_COMMIT must be a 40-character commit" >&2; exit 2; }
raw="${STAGE3_RAW_TEST_JSON:-$(mktemp)}"
nats_container="lites-stage3-agent-nats-${RANDOM}-${RANDOM}"
cleanup() {
  if [[ -z "${STAGE3_RAW_TEST_JSON:-}" ]]; then rm -f "${raw}"; fi
  docker rm -f "${nats_container}" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker run -d --name "${nats_container}" -p 127.0.0.1::4222 \
  nats:2.12.12-alpine@sha256:2ca98656a279b2d88cfdf2b8c3f0d5d7f3941ae9dc2ab12ebaa92d83e0f4ccdb \
  --jetstream --store_dir=/data >/dev/null
for _ in $(seq 1 30); do
  logs="$(docker logs "${nats_container}" 2>&1)"
  [[ "${logs}" == *"Server is ready"* ]] && break
  sleep 1
done
logs="$(docker logs "${nats_container}" 2>&1)"
[[ "${logs}" == *"Server is ready"* ]] || { printf '%s\n' "${logs}"; exit 1; }
nats_port="$(docker port "${nats_container}" 4222/tcp | head -n 1 | sed 's/.*://')"
export NATS_TEST_URL="nats://127.0.0.1:${nats_port}"

LITES_FOUNDATION_TEST_JSON="${raw}" ./scripts/foundation-postgres-smoke.sh
node scripts/write-stage3-execution-delivery-fault-report.mjs \
  --input "${raw}" \
  --source-commit "${source_commit}" \
  --output "${STAGE3_EXECUTION_REPORT:-gate-reports/stage-3/execution-delivery-fault-report.json}"
