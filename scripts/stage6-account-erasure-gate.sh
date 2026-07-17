#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source_commit="${1:-}"
rc_hash="${2:-}"
[[ "${source_commit}" =~ ^[0-9a-f]{40}$ ]] || { echo "source commit is required" >&2; exit 2; }
[[ "${rc_hash}" =~ ^[0-9a-f]{64}$ ]] || { echo "release candidate hash is required" >&2; exit 2; }
[[ -z "$(git -C "${root}" status --porcelain=v1)" ]] || { echo "source worktree must be clean" >&2; exit 2; }
[[ "$(git -C "${root}" rev-parse HEAD)" == "${source_commit}" ]] || { echo "source commit does not match HEAD" >&2; exit 2; }

suffix="$$-${RANDOM}"
minio_name="lites-erasure-minio-${suffix}"
vault_name="lites-erasure-vault-${suffix}"
valkey_name="lites-erasure-valkey-${suffix}"
minio_image="minio/minio:RELEASE.2025-09-07T16-13-09Z@sha256:14cea493d9a34af32f524e538b8346cf79f3321eff8e708c1e2960462bd8936e"
vault_image="hashicorp/vault:1.21.4@sha256:4e33b126a59c0c333b76fb4e894722462659a6bec7c48c9ee8cea56fccfd2569"
valkey_image="valkey/valkey:9.1.0-alpine@sha256:a35428eba9043cc0b79dbe54100f0c92784f2de00ad09b01182bfb1c5c83d1bd"
minio_user="liteserasure"
minio_password="lites-erasure-integration-000000000000"
vault_token="lites-erasure-integration-root"
runtime="${root}/.tmp/stage6-account-erasure-${suffix}"
mkdir -p "${runtime}" "${root}/.tmp/go-cache-stage6" "${root}/.tmp/go-tmp-stage6"

cleanup() {
  docker rm -f "${minio_name}" "${vault_name}" "${valkey_name}" >/dev/null 2>&1 || true
  rm -rf "${runtime}"
}
trap cleanup EXIT

docker run -d --rm --name "${minio_name}" -p 127.0.0.1::9000 \
  -e MINIO_ROOT_USER="${minio_user}" -e MINIO_ROOT_PASSWORD="${minio_password}" \
  "${minio_image}" server /data >/dev/null
docker run -d --rm --name "${vault_name}" -p 127.0.0.1::8200 \
  -e VAULT_DEV_ROOT_TOKEN_ID="${vault_token}" -e VAULT_DEV_LISTEN_ADDRESS=0.0.0.0:8200 \
  "${vault_image}" >/dev/null
docker run -d --rm --name "${valkey_name}" -p 127.0.0.1::6379 "${valkey_image}" >/dev/null

minio_port="$(docker port "${minio_name}" 9000/tcp | sed 's/.*://')"
vault_port="$(docker port "${vault_name}" 8200/tcp | sed 's/.*://')"
valkey_port="$(docker port "${valkey_name}" 6379/tcp | sed 's/.*://')"
for _ in $(seq 1 30); do
  curl --fail --silent "http://127.0.0.1:${minio_port}/minio/health/ready" >/dev/null && break
  sleep 1
done
curl --fail --silent "http://127.0.0.1:${minio_port}/minio/health/ready" >/dev/null
for _ in $(seq 1 30); do
  curl --fail --silent "http://127.0.0.1:${vault_port}/v1/sys/health" >/dev/null && break
  sleep 1
done
curl --fail --silent "http://127.0.0.1:${vault_port}/v1/sys/health" >/dev/null
for _ in $(seq 1 30); do
  docker exec "${valkey_name}" valkey-cli ping 2>/dev/null | grep -qx PONG && break
  sleep 1
done
docker exec "${valkey_name}" valkey-cli ping 2>/dev/null | grep -qx PONG

export GOCACHE="${root}/.tmp/go-cache-stage6" GOTMPDIR="${root}/.tmp/go-tmp-stage6"
export S3_TEST_ENDPOINT="http://127.0.0.1:${minio_port}" S3_TEST_ACCESS_KEY="${minio_user}" S3_TEST_SECRET_KEY="${minio_password}"
export VAULT_TEST_ADDR="http://127.0.0.1:${vault_port}" VAULT_TEST_TOKEN="${vault_token}"
export VALKEY_TEST_ADDRESS="127.0.0.1:${valkey_port}"
dependency_evidence="${root}/.tmp/account-erasure-dependencies.json"
go test -p=1 -json -count=1 -timeout=2m -tags=integration \
  ./internal/objectstore/s3store ./internal/payload/vaultkeys ./internal/identity/erasure \
  -run '^(TestS3CompatiblePurgeRemovesAllVersionsAndDeleteMarkers|TestVaultTransitSubjectKeyPurgeIsIrreversible|TestTaggedSubjectCachePutAndPurgeAreAtomicAndConfined)$' \
  >"${dependency_evidence}"

database_evidence="${root}/.tmp/account-erasure-integration.json"
PATH="$(dirname "$(command -v go)"):${PATH}" \
  LITES_FOUNDATION_TEST_PACKAGES='./internal/identity/postgres' \
  LITES_FOUNDATION_TEST_RUN='^TestAccountErasureHundredSubjectsAndRestoreEpoch$' \
  LITES_FOUNDATION_TEST_TIMEOUT=180s \
  LITES_FOUNDATION_TEST_JSON="${database_evidence}" \
  LITES_FOUNDATION_TEST_QUIET=true \
  "${root}/scripts/foundation-postgres-smoke.sh"

node "${root}/scripts/write-stage6-account-erasure-report.mjs" \
  --source-commit "${source_commit}" --rc-hash "${rc_hash}" \
  --test-json .tmp/account-erasure-integration.json \
  --dependency-test-json .tmp/account-erasure-dependencies.json \
  --s3-runtime "${minio_image}" --vault-runtime "${vault_image}" --valkey-runtime "${valkey_image}"

jq -e --arg commit "${source_commit}" --arg rc "${rc_hash}" \
  '.status=="passed" and .sourceCommit==$commit and .rcHash==$rc and .worktreeDirty==false and .accounts==100 and .totalSurfaceReceiptRowsAfterRestore==1200 and (.dependencyGate.tests|length)==3' \
  "${root}/gate-reports/stage-6/account-erasure-100.json" >/dev/null
printf 'stage-6 account erasure gate: accounts=100 receipts=1200 dependencies=3 status=passed\n'
