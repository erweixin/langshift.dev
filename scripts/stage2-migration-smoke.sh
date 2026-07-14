#!/usr/bin/env bash
set -euo pipefail

cycles="${MIGRATION_SMOKE_CYCLES:-3}"
[[ "${cycles}" =~ ^[0-9]+$ && "${cycles}" -ge 3 ]] || { echo "MIGRATION_SMOKE_CYCLES must be at least 3" >&2; exit 2; }

go_cache="${GOCACHE:-/tmp/lites-go-build}"
go_mod_cache="${GOMODCACHE:-/tmp/lites-go-mod}"
go_tmp="${GOTMPDIR:-/tmp/lites-go-tmp}"
work="$(mktemp -d)"
current_container=""
cleanup() {
  if [[ -n "${current_container}" ]]; then docker rm -f "${current_container}" >/dev/null 2>&1 || true; fi
  rm -rf "${work}"
}
trap cleanup EXIT
mkdir -p "${go_cache}" "${go_mod_cache}" "${go_tmp}"

GOCACHE="${go_cache}" GOMODCACHE="${go_mod_cache}" GOTMPDIR="${go_tmp}" go build -trimpath -o "${work}/lites-migrate" ./cmd/lites-migrate

expected_data=""
postgres_version=""
for cycle in $(seq 1 "${cycles}"); do
  current_container="lites-migration-smoke-${RANDOM}-${RANDOM}"
  docker run -d --name "${current_container}" -p 127.0.0.1::5432 \
    --health-cmd='pg_isready -U postgres -d lites' --health-interval=1s --health-timeout=3s --health-retries=30 \
    -e POSTGRES_PASSWORD=migration_admin -e POSTGRES_DB=lites postgres:16-alpine >/dev/null
  for _ in $(seq 1 40); do
    status="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "${current_container}")"
    [[ "${status}" == "healthy" ]] && break
    if [[ "${status}" == "exited" || "${status}" == "dead" || "${status}" == "unhealthy" ]]; then docker logs "${current_container}"; exit 1; fi
    sleep 1
  done
  [[ "$(docker inspect --format '{{.State.Health.Status}}' "${current_container}")" == "healthy" ]] || { docker logs "${current_container}"; exit 1; }
  port="$(docker port "${current_container}" 5432/tcp | head -n 1 | sed 's/.*://')"
  database_url="postgres://postgres:migration_admin@127.0.0.1:${port}/lites?sslmode=disable"
  ALLOW_INSECURE_DEVELOPMENT=true DATABASE_URL="${database_url}" "${work}/lites-migrate" -direction up >/dev/null
  docker cp contracts/database/900000_verify_contract.sql "${current_container}:/tmp/verify.sql" >/dev/null
  verify_output="$(docker exec "${current_container}" psql -v ON_ERROR_STOP=1 -U postgres -d lites -Atf /tmp/verify.sql)"
  [[ "${verify_output}" == *'"status" : "passed"'* ]] || { printf '%s\n' "${verify_output}"; exit 1; }
  docker exec "${current_container}" psql -v ON_ERROR_STOP=1 -U postgres -d lites -Atc \
    "INSERT INTO identity.users(id,normalized_email,locale,status) VALUES('10000000-0000-4000-8000-000000000001','migration-probe@example.invalid','en','active'); INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES('20000000-0000-4000-8000-000000000001','personal','Migration Probe','active','US','10000000-0000-4000-8000-000000000001');" >/dev/null
  data_before="$(docker exec "${current_container}" psql -v ON_ERROR_STOP=1 -U postgres -d lites -Atc "SELECT id::text||':'||normalized_email FROM identity.users UNION ALL SELECT id::text||':'||name FROM identity.tenants ORDER BY 1")"
  if [[ -z "${expected_data}" ]]; then expected_data="${data_before}"; else [[ "${data_before}" == "${expected_data}" ]] || exit 1; fi
  ALLOW_INSECURE_DEVELOPMENT=true DATABASE_URL="${database_url}" "${work}/lites-migrate" -direction down -steps 1 >/dev/null
  data_after_down="$(docker exec "${current_container}" psql -v ON_ERROR_STOP=1 -U postgres -d lites -Atc "SELECT id::text||':'||normalized_email FROM identity.users UNION ALL SELECT id::text||':'||name FROM identity.tenants ORDER BY 1")"
  [[ "${data_after_down}" == "${data_before}" ]] || exit 1
  ALLOW_INSECURE_DEVELOPMENT=true DATABASE_URL="${database_url}" "${work}/lites-migrate" -direction up >/dev/null
  data_after_up="$(docker exec "${current_container}" psql -v ON_ERROR_STOP=1 -U postgres -d lites -Atc "SELECT id::text||':'||normalized_email FROM identity.users UNION ALL SELECT id::text||':'||name FROM identity.tenants ORDER BY 1")"
  [[ "${data_after_up}" == "${data_before}" ]] || exit 1
  if ALLOW_INSECURE_DEVELOPMENT=true DATABASE_URL="${database_url}" "${work}/lites-migrate" -direction down -steps 3 >/dev/null 2>&1; then
    echo "irreversible baseline rollback unexpectedly succeeded" >&2
    exit 1
  fi
  status_output="$(ALLOW_INSECURE_DEVELOPMENT=true DATABASE_URL="${database_url}" "${work}/lites-migrate" -direction status)"
  [[ "${status_output}" == *'"current_version":3'* ]] || { printf '%s\n' "${status_output}"; exit 1; }
  postgres_version="$(docker exec "${current_container}" psql -U postgres -d lites -Atc "SHOW server_version")"
  docker rm -f "${current_container}" >/dev/null
  current_container=""
done

data_checksum="$(printf '%s' "${expected_data}" | sha256sum | awk '{print $1}')"
if [[ -n "${MIGRATION_REPORT_SOURCE_COMMIT:-}" ]]; then
  node scripts/write-stage2-migration-report.mjs --source-commit "${MIGRATION_REPORT_SOURCE_COMMIT}" --cycles "${cycles}" --data-checksum "${data_checksum}" --postgres-version "${postgres_version}"
else
  printf 'stage-2 migration smoke: cycles=%s status=passed data=%s\n' "${cycles}" "${data_checksum}"
fi
