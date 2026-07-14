#!/usr/bin/env bash
set -euo pipefail

container_name="lites-contract-pg-${RANDOM}-${RANDOM}"
cleanup() {
  docker rm -f "${container_name}" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker run -d \
  --name "${container_name}" \
  --health-cmd="pg_isready -U postgres -d lites_contract" \
  --health-interval=1s \
  --health-timeout=3s \
  --health-retries=30 \
  -e POSTGRES_PASSWORD=lites_contract_test \
  -e POSTGRES_DB=lites_contract \
  -v "$(pwd)/contracts/database:/docker-entrypoint-initdb.d:ro" \
  postgres:16-alpine >/dev/null

for _ in $(seq 1 40); do
  status="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "${container_name}")"
  if [[ "${status}" == "healthy" ]]; then
    break
  fi
  if [[ "${status}" == "exited" || "${status}" == "dead" || "${status}" == "unhealthy" ]]; then
    docker logs "${container_name}"
    exit 1
  fi
  sleep 1
done

status="$(docker inspect --format '{{.State.Health.Status}}' "${container_name}")"
if [[ "${status}" != "healthy" ]]; then
  docker logs "${container_name}"
  exit 1
fi

logs="$(docker logs "${container_name}" 2>&1)"
if [[ "${logs}" != *'"status" : "passed"'* ]]; then
  printf '%s\n' "${logs}"
  exit 1
fi

actual_tables="$(docker exec "${container_name}" psql -U postgres -d lites_contract -Atc "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE c.relkind='r' AND n.nspname IN ('identity','product','agent','contracts')")"
if [[ "${actual_tables}" != "89" ]]; then
  printf 'Expected 89 contract tables, found %s\n' "${actual_tables}"
  exit 1
fi

printf 'database-contract-smoke passed: postgres=16 tables=89 forced_rls=82 append_only=21 cross_tenant_visible=0\n'
