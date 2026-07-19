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

for _ in $(seq 1 60); do
  logs="$(docker logs "${container_name}" 2>&1)"
  [[ "${logs}" == *"PostgreSQL init process complete; ready for start up."* ]] && break
  sleep 1
done
logs="$(docker logs "${container_name}" 2>&1)"
if [[ "${logs}" != *"PostgreSQL init process complete; ready for start up."* ]]; then
  printf '%s\n' "${logs}"
  exit 1
fi
if [[ "${logs}" != *'"status" : "passed"'* ]]; then
  printf '%s\n' "${logs}"
  exit 1
fi

catalog_counts="$(node -e 'const catalog=require("./contracts/database/schema-catalog.json"); process.stdout.write([catalog.tables.length,catalog.tables.filter(table=>table.tenantScoped).length,catalog.tables.filter(table=>table.appendOnly).length].join(":"))')"
IFS=: read -r expected_tables expected_forced_rls expected_append_only <<<"${catalog_counts}"
actual_tables="$(docker exec "${container_name}" psql -U postgres -d lites_contract -Atc "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE c.relkind='r' AND n.nspname IN ('identity','product','agent','contracts')")"
if [[ "${actual_tables}" != "${expected_tables}" ]]; then
  printf 'Expected %s contract tables, found %s\n' "${expected_tables}" "${actual_tables}"
  exit 1
fi

actual_forced_rls="$(docker exec "${container_name}" psql -U postgres -d lites_contract -Atc "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE c.relkind='r' AND n.nspname IN ('identity','product','agent','contracts') AND c.relrowsecurity AND c.relforcerowsecurity")"
actual_append_only="$(docker exec "${container_name}" psql -U postgres -d lites_contract -Atc "SELECT count(*) FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE NOT t.tgisinternal AND t.tgname LIKE '%_append_only' AND n.nspname IN ('identity','product','agent','contracts')")"
if [[ "${actual_forced_rls}" != "${expected_forced_rls}" ]]; then
  printf 'Expected %s forced-RLS tables, found %s\n' "${expected_forced_rls}" "${actual_forced_rls}"
  exit 1
fi
if [[ "${actual_append_only}" != "${expected_append_only}" ]]; then
  printf 'Expected %s append-only triggers, found %s\n' "${expected_append_only}" "${actual_append_only}"
  exit 1
fi
postgres_version="$(docker exec "${container_name}" psql -U postgres -d lites_contract -Atc 'SHOW server_version')"
database_image_digest="$(docker image inspect postgres:16-alpine --format '{{index .RepoDigests 0}}')"

POSTGRES_VERSION="${postgres_version}" \
DATABASE_IMAGE_DIGEST="${database_image_digest}" \
TABLE_COUNT="${actual_tables}" \
FORCED_RLS_COUNT="${actual_forced_rls}" \
APPEND_ONLY_TRIGGER_COUNT="${actual_append_only}" \
node scripts/write-database-smoke-report.mjs

printf 'database-contract-smoke passed: postgres=%s tables=%s forced_rls=%s append_only=%s cross_tenant_visible=0\n' "${postgres_version}" "${actual_tables}" "${actual_forced_rls}" "${actual_append_only}"
