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
  docker cp deploy/migrations/900039_verify_current.sql "${current_container}:/tmp/verify.sql" >/dev/null
  verify_output="$(docker exec "${current_container}" psql -v ON_ERROR_STOP=1 -U postgres -d lites -Atf /tmp/verify.sql)"
  [[ "${verify_output}" == *'"status" : "passed"'* ]] || { printf '%s\n' "${verify_output}"; exit 1; }
  docker exec "${current_container}" psql -v ON_ERROR_STOP=1 -U postgres -d lites -Atc \
    "INSERT INTO identity.users(id,normalized_email,locale,status) VALUES('10000000-0000-4000-8000-000000000001','migration-probe@example.invalid','en','active'); INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES('20000000-0000-4000-8000-000000000001','personal','Migration Probe','active','US','10000000-0000-4000-8000-000000000001'); INSERT INTO agent.runs(id,tenant_id,user_id,conversation_id,status,run_version,due_at,profile_snapshot_id,budget_snapshot) VALUES('30000000-0000-4000-8000-000000000001','20000000-0000-4000-8000-000000000001','10000000-0000-4000-8000-000000000001','30000000-0000-4000-8000-000000000002','accepted',1,CURRENT_TIMESTAMP+interval '1 hour','migration-probe@v9','{}'); INSERT INTO agent.events(id,tenant_id,user_id,seq,event_type,event_schema_version,aggregate_kind,aggregate_id,aggregate_version,store_epoch,occurred_at,committed_at,actor,correlation_id,payload_ref,payload_hash) VALUES('40000000-0000-4000-8000-000000000001','20000000-0000-4000-8000-000000000001','10000000-0000-4000-8000-000000000001',1,'ToolCallSucceeded',1,'tool_call','50000000-0000-4000-8000-000000000001',1,'60000000-0000-4000-8000-000000000001',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,'{\"kind\":\"system\"}','70000000-0000-4000-8000-000000000001','encrypted://migration/event','migration-event'); INSERT INTO agent.tool_calls(id,tenant_id,user_id,run_id,status,tool_call_version,tool_name,descriptor_snapshot_id,normalized_input_ref,request_hash,effect_class,effect_key,result_event_id) VALUES('50000000-0000-4000-8000-000000000001','20000000-0000-4000-8000-000000000001','10000000-0000-4000-8000-000000000001','30000000-0000-4000-8000-000000000001','succeeded',1,'migration_probe','migration_probe@v1','encrypted://migration/input','migration-request','idempotent_write','migration-effect','40000000-0000-4000-8000-000000000001'); INSERT INTO agent.tool_effects(id,tenant_id,effect_scope,provider_id,tool_name,effect_key,request_hash,status,tool_call_id,run_id,effect_class) VALUES('50000000-0000-4000-8000-000000000002','20000000-0000-4000-8000-000000000001','tenant:migration','migration-provider','migration_probe','migration-effect','migration-request','prepared','50000000-0000-4000-8000-000000000001','30000000-0000-4000-8000-000000000001','idempotent_write'); INSERT INTO agent.outbox(id,tenant_id,command_id,command_type,aggregate_kind,aggregate_id,store_epoch,payload_ref,payload_hash,status,available_at) VALUES('80000000-0000-4000-8000-000000000001','20000000-0000-4000-8000-000000000001','80000000-0000-4000-8000-000000000002','ResumeAgentRun','run','30000000-0000-4000-8000-000000000001','60000000-0000-4000-8000-000000000001','encrypted://migration/resume','migration-resume','pending',CURRENT_TIMESTAMP); INSERT INTO agent.continuations(id,tenant_id,run_id,run_version,group_kind,group_id,command_id,status,continuation_kind) VALUES('90000000-0000-4000-8000-000000000001','20000000-0000-4000-8000-000000000001','30000000-0000-4000-8000-000000000001',1,'parallel','90000000-0000-4000-8000-000000000002','80000000-0000-4000-8000-000000000002','committed','resume');" >/dev/null
  data_query="SELECT id::text||':'||normalized_email FROM identity.users UNION ALL SELECT id::text||':'||name FROM identity.tenants UNION ALL SELECT id::text||':'||COALESCE(result_event_id::text,'') FROM agent.tool_calls UNION ALL SELECT id::text||':'||COALESCE(effect_class,'') FROM agent.tool_effects UNION ALL SELECT id::text||':'||COALESCE(continuation_kind,'') FROM agent.continuations ORDER BY 1"
  data_before="$(docker exec "${current_container}" psql -v ON_ERROR_STOP=1 -U postgres -d lites -Atc "${data_query}")"
  if [[ -z "${expected_data}" ]]; then expected_data="${data_before}"; else [[ "${data_before}" == "${expected_data}" ]] || exit 1; fi
  ALLOW_INSECURE_DEVELOPMENT=true DATABASE_URL="${database_url}" "${work}/lites-migrate" -direction down -steps 1 >/dev/null
  data_after_down="$(docker exec "${current_container}" psql -v ON_ERROR_STOP=1 -U postgres -d lites -Atc "${data_query}")"
  [[ "${data_after_down}" == "${data_before}" ]] || exit 1
  ALLOW_INSECURE_DEVELOPMENT=true DATABASE_URL="${database_url}" "${work}/lites-migrate" -direction up >/dev/null
  data_after_up="$(docker exec "${current_container}" psql -v ON_ERROR_STOP=1 -U postgres -d lites -Atc "${data_query}")"
  [[ "${data_after_up}" == "${data_before}" ]] || exit 1
  if ALLOW_INSECURE_DEVELOPMENT=true DATABASE_URL="${database_url}" "${work}/lites-migrate" -direction down -steps 39 >/dev/null 2>&1; then
    echo "irreversible baseline rollback unexpectedly succeeded" >&2
    exit 1
  fi
  status_output="$(ALLOW_INSECURE_DEVELOPMENT=true DATABASE_URL="${database_url}" "${work}/lites-migrate" -direction status)"
  [[ "${status_output}" == *'"current_version":39'* ]] || { printf '%s\n' "${status_output}"; exit 1; }
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
