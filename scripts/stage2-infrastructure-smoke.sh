#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
compose_file="${root}/deploy/compose/foundation.compose.yaml"
runtime="$(mktemp -d "${TMPDIR:-/tmp}/lites-foundation.XXXXXX")"
project="lites-foundation-${RANDOM}-${RANDOM}"
keep="${KEEP_STAGE2_INFRASTRUCTURE:-false}"
umask 077

cleanup() {
  if [[ "${keep}" != "true" ]]; then
    docker compose --project-name "${project}" --file "${compose_file}" down --volumes --remove-orphans >/dev/null 2>&1 || true
    rm -rf "${runtime}"
  else
    printf 'preserved project=%s runtime=%s\n' "${project}" "${runtime}"
  fi
}
trap cleanup EXIT

random_secret() { openssl rand -hex 32; }
random_secret >"${runtime}/postgres-password"
random_secret >"${runtime}/minio-root-user"
random_secret >"${runtime}/minio-root-password"
random_secret >"${runtime}/grafana-admin-password"
export POSTGRES_PASSWORD_FILE="${runtime}/postgres-password"
export MINIO_ROOT_USER_FILE="${runtime}/minio-root-user"
export MINIO_ROOT_PASSWORD_FILE="${runtime}/minio-root-password"
export GRAFANA_ADMIN_PASSWORD_FILE="${runtime}/grafana-admin-password"
export NATS_USER="lites"
export NATS_PASSWORD="$(random_secret)"
export VALKEY_PASSWORD="$(random_secret)"
export VAULT_DEV_ROOT_TOKEN="$(random_secret)"

compose=(docker compose --project-name "${project}" --file "${compose_file}")
"${compose[@]}" --profile tools config --format json >"${runtime}/resolved-compose.json"
node "${root}/scripts/validate-stage2-foundation-compose.mjs" "${runtime}/resolved-compose.json"
"${compose[@]}" up --detach --wait --wait-timeout 120

postgres_password="$(<"${POSTGRES_PASSWORD_FILE}")"
postgres_counts="$({ "${compose[@]}" exec -T -e PGPASSWORD="${postgres_password}" postgres psql -U postgres -d lites -Atc "SELECT (SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE c.relkind='r' AND n.nspname IN ('identity','product','agent','contracts')) || ':' || (SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE c.relkind='r' AND n.nspname IN ('identity','product','agent','contracts') AND c.relrowsecurity AND c.relforcerowsecurity);"; } 2>/dev/null)"
[[ "${postgres_counts}" == "92:85" ]]
"${compose[@]}" exec -T -e PGPASSWORD="${postgres_password}" postgres psql -v ON_ERROR_STOP=1 -U postgres -d lites -c "CREATE TEMP TABLE infrastructure_smoke(value text NOT NULL); INSERT INTO infrastructure_smoke VALUES ('postgres-ok'); SELECT value FROM infrastructure_smoke;" >/dev/null

nats_url="nats://${NATS_USER}:${NATS_PASSWORD}@nats:4222"
"${compose[@]}" --profile tools run --rm -e NATS_URL="${nats_url}" nats-box nats stream add LITES_COMMANDS --subjects "commands.>" --storage file --retention limits --replicas 1 --defaults >/dev/null
printf 'jetstream-ok' | "${compose[@]}" --profile tools run -T --rm -e NATS_URL="${nats_url}" nats-box nats pub commands.smoke >/dev/null
"${compose[@]}" --profile tools run --rm -e NATS_URL="${nats_url}" nats-box nats stream info LITES_COMMANDS --json >"${runtime}/nats-stream.json"
node -e 'const x=require(process.argv[1]); if(x.state.messages!==1) process.exit(1)' "${runtime}/nats-stream.json"

"${compose[@]}" exec -T -e VALKEYCLI_AUTH="${VALKEY_PASSWORD}" valkey valkey-cli SET lites:smoke valkey-ok EX 60 >/dev/null
[[ "$("${compose[@]}" exec -T -e VALKEYCLI_AUTH="${VALKEY_PASSWORD}" valkey valkey-cli --raw GET lites:smoke)" == "valkey-ok" ]]

minio_user="$(<"${MINIO_ROOT_USER_FILE}")"
minio_password="$(<"${MINIO_ROOT_PASSWORD_FILE}")"
minio_host="http://${minio_user}:${minio_password}@minio:9000"
for bucket in lites-payloads lites-artifacts lites-identity-imports; do
  "${compose[@]}" --profile tools run --rm -e MC_HOST_lites="${minio_host}" minio-client mb --ignore-existing "lites/${bucket}" >/dev/null
  "${compose[@]}" --profile tools run --rm -e MC_HOST_lites="${minio_host}" minio-client version enable "lites/${bucket}" >/dev/null
done
printf 's3-ok' | "${compose[@]}" --profile tools run -T --rm -e MC_HOST_lites="${minio_host}" minio-client pipe lites/lites-payloads/smoke/object >/dev/null
[[ "$("${compose[@]}" --profile tools run --rm -e MC_HOST_lites="${minio_host}" minio-client cat lites/lites-payloads/smoke/object)" == "s3-ok" ]]

"${compose[@]}" exec -T -e VAULT_TOKEN="${VAULT_DEV_ROOT_TOKEN}" vault vault kv put secret/lites/smoke value=vault-ok >/dev/null
[[ "$("${compose[@]}" exec -T -e VAULT_TOKEN="${VAULT_DEV_ROOT_TOKEN}" vault vault kv get -field=value secret/lites/smoke)" == "vault-ok" ]]

for endpoint in \
  http://otel-collector:13133/ \
  http://tempo:3200/ready \
  http://loki:3100/ready \
  http://prometheus:9090/-/ready \
  http://grafana:3000/api/health; do
  if ! "${compose[@]}" --profile tools run --rm probe --fail --silent \
    --retry 20 --retry-delay 1 --retry-all-errors "${endpoint}" >/dev/null 2>&1; then
    printf 'infrastructure endpoint not ready: %s\n' "${endpoint}" >&2
    exit 1
  fi
done

trace_id="10000000000000000000000000000001"
span_id="2000000000000001"
now_ns="$(($(date +%s) * 1000000000))"
end_ns="$((now_ns + 1000000))"
printf '{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"foundation-smoke"}}]},"scopeSpans":[{"scope":{"name":"foundation-smoke"},"spans":[{"traceId":"%s","spanId":"%s","name":"foundation-smoke","kind":1,"startTimeUnixNano":"%s","endTimeUnixNano":"%s","status":{"code":1}}]}]}]}' "${trace_id}" "${span_id}" "${now_ns}" "${end_ns}" | \
  "${compose[@]}" --profile tools run -T --rm probe --fail --silent --show-error -H 'Content-Type: application/json' --data-binary @- http://otel-collector:4318/v1/traces >/dev/null

trace_found=false
for _ in $(seq 1 20); do
  if "${compose[@]}" --profile tools run --rm probe --fail --silent "http://tempo:3200/api/traces/${trace_id}" >"${runtime}/tempo-trace.json" 2>/dev/null; then
    trace_found=true
    break
  fi
  sleep 1
done
[[ "${trace_found}" == "true" ]]
grep -q 'foundation-smoke' "${runtime}/tempo-trace.json"

if [[ -n "${INFRASTRUCTURE_REPORT_SOURCE_COMMIT:-}" ]]; then
  node "${root}/scripts/write-stage2-infrastructure-report.mjs" \
    --source-commit "${INFRASTRUCTURE_REPORT_SOURCE_COMMIT}" \
    --resolved-compose "${runtime}/resolved-compose.json"
fi

printf 'stage-2 infrastructure smoke: postgres=%s jetstream=1 valkey=1 s3=1 vault=1 otlp_trace=1 observability=4 status=passed\n' "${postgres_counts}"
