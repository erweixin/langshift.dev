#!/usr/bin/env bash
set -euo pipefail

container_name="lites-foundation-pg-${RANDOM}-${RANDOM}"
go_cache="${GOCACHE:-/tmp/lites-go-build}"
go_mod_cache="${GOMODCACHE:-/tmp/lites-go-mod}"
go_tmp="${GOTMPDIR:-/tmp/lites-go-tmp}"
mkdir -p "${go_cache}" "${go_mod_cache}" "${go_tmp}"
cleanup() { docker rm -f "${container_name}" >/dev/null 2>&1 || true; }
trap cleanup EXIT

docker run -d \
  --name "${container_name}" \
  -p 127.0.0.1::5432 \
  --health-cmd="pg_isready -U postgres -d lites_foundation" \
  --health-interval=1s --health-timeout=3s --health-retries=30 \
  -e POSTGRES_PASSWORD=foundation_admin \
  -e POSTGRES_DB=lites_foundation \
  -v "$(pwd)/contracts/database:/docker-entrypoint-initdb.d:ro" \
  postgres:16-alpine >/dev/null

for _ in $(seq 1 40); do
  status="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "${container_name}")"
  [[ "${status}" == "healthy" ]] && break
  if [[ "${status}" == "exited" || "${status}" == "dead" || "${status}" == "unhealthy" ]]; then docker logs "${container_name}"; exit 1; fi
  sleep 1
done
[[ "$(docker inspect --format '{{.State.Health.Status}}' "${container_name}")" == "healthy" ]] || { docker logs "${container_name}"; exit 1; }

for _ in $(seq 1 60); do
  logs="$(docker logs "${container_name}" 2>&1)"
  [[ "${logs}" == *"PostgreSQL init process complete; ready for start up."* ]] && break
  sleep 1
done
logs="$(docker logs "${container_name}" 2>&1)"
[[ "${logs}" == *"PostgreSQL init process complete; ready for start up."* ]] || { printf '%s\n' "${logs}"; exit 1; }
for _ in $(seq 1 30); do
  docker exec "${container_name}" psql -U postgres -d lites_foundation -Atc 'SELECT 1' >/dev/null 2>&1 && break
  sleep 1
done
docker exec "${container_name}" psql -U postgres -d lites_foundation -Atc 'SELECT 1' >/dev/null

for migration in \
  deploy/migrations/000002_schema_contract_metadata.up.sql \
  deploy/migrations/000003_execution_kernel_constraints.up.sql \
  deploy/migrations/000004_execution_ownership_constraints.up.sql \
  deploy/migrations/000005_scheduler_job_metadata.up.sql \
  deploy/migrations/000006_scheduler_dispatch_protocol.up.sql \
  deploy/migrations/000007_parallel_group_contract.up.sql \
  deploy/migrations/000008_tool_execution_contract.up.sql \
  deploy/migrations/000009_tool_effect_ledger.up.sql \
  deploy/migrations/000010_effect_reconciliation_contract.up.sql \
  deploy/migrations/000011_effect_reconciliation_retry.up.sql \
  deploy/migrations/000012_command_redelivery_protocol.up.sql \
  deploy/migrations/000013_expired_effect_sweeper.up.sql \
  deploy/migrations/000014_repair_resolution_contract.up.sql \
  deploy/migrations/000015_repair_recovery_scan.up.sql; do
  target="/tmp/$(basename "${migration}")"
  docker cp "${migration}" "${container_name}:${target}" >/dev/null
  docker exec "${container_name}" psql -v ON_ERROR_STOP=1 -U postgres -d lites_foundation -f "${target}" >/dev/null
done

docker exec "${container_name}" psql -v ON_ERROR_STOP=1 -U postgres -d lites_foundation -c \
  "CREATE ROLE lites_identity_service LOGIN PASSWORD 'foundation_service' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS; GRANT CONNECT ON DATABASE lites_foundation TO lites_identity_service; GRANT USAGE ON SCHEMA identity, product, agent TO lites_identity_service; GRANT SELECT, INSERT, UPDATE ON identity.users, identity.password_credentials, identity.email_verifications, identity.password_reset_requests, identity.sessions, identity.memberships, identity.membership_imports, identity.account_erasure_requests, identity.invitations, identity.invitation_imports, identity.anonymous_subjects, identity.onboarding_sessions, identity.onboarding_claims TO lites_identity_service; GRANT SELECT, INSERT ON identity.anonymous_erasure_receipts TO lites_identity_service; GRANT SELECT, INSERT ON identity.tenants TO lites_identity_service; GRANT INSERT ON identity.security_events TO lites_identity_service; GRANT EXECUTE ON FUNCTION identity.lookup_invitation_for_acceptance(text,bytea), identity.lock_active_tenant(uuid), agent.list_ready_outbox_tenants(uuid,uuid,integer,integer,integer) TO lites_identity_service; GRANT SELECT ON product.role_profiles TO lites_identity_service; GRANT SELECT, INSERT, UPDATE, DELETE ON product.missions, product.route_revisions TO lites_identity_service; GRANT SELECT, INSERT ON product.mission_imports TO lites_identity_service; GRANT SELECT, INSERT, UPDATE ON product.data_export_requests TO lites_identity_service; GRANT SELECT, INSERT, UPDATE ON agent.idempotency_responses, agent.event_cursors, agent.outbox, agent.inbox TO lites_identity_service; GRANT SELECT, INSERT ON agent.events TO lites_identity_service;" >/dev/null

docker exec "${container_name}" psql -v ON_ERROR_STOP=1 -U postgres -d lites_foundation -c \
  "CREATE ROLE lites_agent_service LOGIN PASSWORD 'foundation_agent_service' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS; GRANT CONNECT ON DATABASE lites_foundation TO lites_agent_service; GRANT USAGE ON SCHEMA identity, agent TO lites_agent_service; GRANT SELECT ON identity.sessions, identity.memberships TO lites_agent_service; GRANT SELECT, INSERT, UPDATE ON agent.runs, agent.jobs, agent.job_attempts, agent.inbox, agent.event_cursors, agent.outbox, agent.idempotency_responses, agent.tool_calls, agent.tool_effects, agent.parallel_groups, agent.parallel_group_members, agent.continuations, agent.repair_commands TO lites_agent_service; GRANT SELECT, INSERT ON agent.events, agent.repair_approvals TO lites_agent_service; GRANT EXECUTE ON FUNCTION agent.list_command_reconciliation_tenants(uuid,uuid,integer,integer,integer,timestamptz,timestamptz), agent.list_expired_tool_effect_tenants(uuid,uuid,integer,integer,integer,timestamptz), agent.list_recoverable_repair_tenants(uuid,uuid,integer,integer,integer,timestamptz) TO lites_agent_service;" >/dev/null

docker exec "${container_name}" psql -v ON_ERROR_STOP=1 -U postgres -d lites_foundation -c \
  "CREATE ROLE lites_scheduler_service LOGIN PASSWORD 'foundation_scheduler_service' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS; GRANT CONNECT ON DATABASE lites_foundation TO lites_scheduler_service; GRANT USAGE ON SCHEMA agent TO lites_scheduler_service; GRANT SELECT,UPDATE ON agent.jobs TO lites_scheduler_service; GRANT EXECUTE ON FUNCTION agent.scheduler_claim_resource(text,text,bytea,timestamptz,timestamptz), agent.scheduler_list_ready_jobs(text,text,bigint,bytea,uuid,timestamptz,integer), agent.scheduler_commit_dispatch(text,text,bigint,bytea,jsonb,jsonb,timestamptz,timestamptz), agent.scheduler_abort_resource(text,text,bigint,bytea,timestamptz) TO lites_scheduler_service;" >/dev/null

container_port="$(docker port "${container_name}" 5432/tcp | head -n 1 | sed 's/.*://')"

export LITES_TEST_ADMIN_DATABASE_URL="postgres://postgres:foundation_admin@127.0.0.1:${container_port}/lites_foundation?sslmode=disable"
export LITES_TEST_IDENTITY_DATABASE_URL="postgres://lites_identity_service:foundation_service@127.0.0.1:${container_port}/lites_foundation?sslmode=disable"
export LITES_TEST_AGENT_DATABASE_URL="postgres://lites_agent_service:foundation_agent_service@127.0.0.1:${container_port}/lites_foundation?sslmode=disable"
export LITES_TEST_SCHEDULER_DATABASE_URL="postgres://lites_scheduler_service:foundation_scheduler_service@127.0.0.1:${container_port}/lites_foundation?sslmode=disable"
export GOCACHE="${go_cache}" GOMODCACHE="${go_mod_cache}" GOTMPDIR="${go_tmp}"
if [[ -n "${LITES_FOUNDATION_TEST_JSON:-}" ]]; then
  go test -json -count=1 -timeout=60s -tags=integration ./internal/identity/postgres ./internal/identity/mail ./internal/eventstore/postgres ./internal/execution/postgres | tee "${LITES_FOUNDATION_TEST_JSON}"
else
  go test -count=1 -timeout=60s -tags=integration ./internal/identity/postgres ./internal/identity/mail ./internal/eventstore/postgres ./internal/execution/postgres
fi
