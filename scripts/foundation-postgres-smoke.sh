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

while IFS= read -r migration; do
  target="/tmp/$(basename "${migration}")"
  docker cp "${migration}" "${container_name}:${target}" >/dev/null
  docker exec "${container_name}" psql -v ON_ERROR_STOP=1 -U postgres -d lites_foundation -f "${target}" >/dev/null
done < <(jq -er '.migrations[] | select(.version > 1) | .up' deploy/migrations/manifest.json)

docker exec "${container_name}" psql -v ON_ERROR_STOP=1 -U postgres -d lites_foundation -c \
  "CREATE ROLE lites_identity_service LOGIN PASSWORD 'foundation_service' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS; GRANT CONNECT ON DATABASE lites_foundation TO lites_identity_service; GRANT USAGE ON SCHEMA identity, product, agent TO lites_identity_service; GRANT SELECT, INSERT, UPDATE ON identity.users, identity.password_credentials, identity.email_verifications, identity.password_reset_requests, identity.sessions, identity.memberships, identity.membership_imports, identity.account_erasure_requests, identity.invitations, identity.invitation_imports, identity.anonymous_subjects, identity.onboarding_sessions, identity.onboarding_claims TO lites_identity_service; GRANT SELECT, INSERT ON identity.anonymous_erasure_receipts TO lites_identity_service; GRANT SELECT, INSERT ON identity.tenants TO lites_identity_service; GRANT INSERT ON identity.security_events TO lites_identity_service; GRANT EXECUTE ON FUNCTION identity.lookup_invitation_for_acceptance(text,bytea), identity.lock_active_tenant(uuid), agent.list_ready_outbox_tenants(uuid,uuid,integer,integer,integer) TO lites_identity_service; GRANT SELECT ON product.role_profiles TO lites_identity_service; GRANT SELECT, INSERT, UPDATE, DELETE ON product.missions, product.route_revisions TO lites_identity_service; GRANT SELECT, INSERT ON product.mission_imports TO lites_identity_service; GRANT SELECT, INSERT, UPDATE ON product.data_export_requests TO lites_identity_service; GRANT SELECT, INSERT, UPDATE ON agent.idempotency_responses, agent.event_cursors, agent.outbox, agent.inbox, agent.repair_commands TO lites_identity_service; GRANT DELETE ON agent.idempotency_responses TO lites_identity_service; GRANT SELECT, INSERT ON agent.events, agent.repair_approvals TO lites_identity_service;" >/dev/null

docker exec "${container_name}" psql -v ON_ERROR_STOP=1 -U postgres -d lites_foundation -c \
  "CREATE ROLE lites_agent_service LOGIN PASSWORD 'foundation_agent_service' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS; GRANT CONNECT ON DATABASE lites_foundation TO lites_agent_service; GRANT USAGE ON SCHEMA identity, product, agent, contracts TO lites_agent_service; GRANT SELECT ON identity.sessions, identity.memberships, product.byok_credentials, product.byok_credential_versions TO lites_agent_service; GRANT SELECT, INSERT, UPDATE ON agent.runs, agent.run_cancellations, agent.child_group_cancellations, agent.jobs, agent.job_attempts, agent.inbox, agent.event_cursors, agent.outbox, agent.idempotency_responses, agent.tool_calls, agent.tool_effects, agent.parallel_groups, agent.parallel_group_members, agent.child_groups, agent.child_group_members, agent.orchestration_quotas, agent.continuations, agent.repair_commands, agent.approvals, agent.workspace_revision_commits, agent.llm_attempts, agent.llm_provider_attempts TO lites_agent_service; GRANT SELECT,INSERT ON agent.run_messages,agent.tool_proposals TO lites_agent_service; GRANT SELECT ON agent.runtime_sessions TO lites_agent_service; GRANT SELECT, INSERT ON agent.events, agent.repair_approvals, agent.approval_decisions TO lites_agent_service; GRANT SELECT ON contracts.credit_buckets TO lites_agent_service; GRANT SELECT,INSERT,UPDATE ON contracts.usage_reservations TO lites_agent_service; GRANT SELECT,INSERT ON contracts.usage_ledger,contracts.provider_costs TO lites_agent_service; GRANT EXECUTE ON FUNCTION agent.list_command_reconciliation_tenants(uuid,uuid,integer,integer,integer,timestamptz,timestamptz), agent.list_expired_tool_effect_tenants(uuid,uuid,integer,integer,integer,timestamptz), agent.list_recoverable_repair_tenants(uuid,uuid,integer,integer,integer,timestamptz), agent.list_expired_repair_tenants(uuid,uuid,integer,integer,integer,timestamptz), agent.list_expired_approval_tenants(uuid,uuid,integer,integer,integer,timestamptz), agent.list_stale_approval_tenants(uuid,uuid,integer,integer,integer), agent.list_workspace_recovery_tenants(uuid,uuid,integer,integer,integer,timestamptz), agent.list_recoverable_llm_provider_tenants(uuid,uuid,integer,integer,integer,timestamptz), agent.list_run_cancellation_tenants(uuid,uuid,integer,integer,integer,timestamptz), agent.lock_authorized_artifact_export(uuid,uuid,uuid,text,uuid,text), contracts.list_releasable_usage_tenants(uuid,uuid,integer,integer,integer,timestamptz), contracts.reserve_credit_units(uuid,uuid,bigint,timestamptz,timestamptz), contracts.settle_credit_units(uuid,uuid,bigint,bigint,timestamptz), contracts.release_credit_units(uuid,uuid,bigint,timestamptz) TO lites_agent_service;" >/dev/null

docker exec "${container_name}" psql -v ON_ERROR_STOP=1 -U postgres -d lites_foundation -c \
  "GRANT SELECT ON product.memory_policies,product.projects,product.share_grants TO lites_agent_service; GRANT SELECT,INSERT,UPDATE ON agent.memory_documents,agent.memory_index_projections TO lites_agent_service; GRANT SELECT,INSERT ON agent.memory_document_revisions,agent.memory_revision_sources,agent.memory_revision_subjects,agent.memory_revision_derivations,agent.memory_tombstones,agent.retrieval_manifests,agent.retrieval_manifest_chunks,agent.memory_accesses TO lites_agent_service;" >/dev/null

docker exec "${container_name}" psql -v ON_ERROR_STOP=1 -U postgres -d lites_foundation -c \
  "GRANT SELECT ON agent.behavior_snapshots,agent.behavior_channel_deployments TO lites_agent_service;" >/dev/null

docker exec "${container_name}" psql -v ON_ERROR_STOP=1 -U postgres -d lites_foundation -c \
  "GRANT SELECT,INSERT,UPDATE ON agent.conversations TO lites_agent_service; GRANT SELECT,INSERT ON agent.coach_context_snapshots TO lites_agent_service; GRANT EXECUTE ON FUNCTION agent.lock_active_owned_mission(uuid,uuid,uuid), agent.lock_active_focused_mission(uuid,uuid,uuid), agent.read_coach_context_snapshot(uuid,uuid,uuid), agent.lock_coach_context_fence(uuid,uuid,uuid,uuid,bigint,bigint,bigint,text,uuid,bigint,uuid,bigint,bigint) TO lites_agent_service;" >/dev/null

docker exec "${container_name}" psql -v ON_ERROR_STOP=1 -U postgres -d lites_foundation -c \
  "GRANT SELECT ON agent.platform_tool_execution_overlays,agent.tenant_tool_execution_overlays,agent.runtime_policy_snapshots TO lites_agent_service; GRANT SELECT,INSERT ON agent.runtime_sessions TO lites_agent_service;" >/dev/null

docker exec "${container_name}" psql -v ON_ERROR_STOP=1 -U postgres -d lites_foundation -c \
  "CREATE ROLE lites_product_service LOGIN PASSWORD 'foundation_product_service' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS; GRANT CONNECT ON DATABASE lites_foundation TO lites_product_service; GRANT USAGE ON SCHEMA identity,product,agent TO lites_product_service; GRANT SELECT ON identity.memberships TO lites_product_service; GRANT SELECT ON product.role_profiles,product.capability_claims TO lites_product_service; GRANT SELECT,INSERT,UPDATE ON product.missions,product.mission_focuses,product.route_revisions TO lites_product_service; GRANT SELECT,INSERT,UPDATE ON product.projects,product.project_milestones,product.project_workspace_bindings,product.artifacts,product.portfolio_exports,product.share_grants TO lites_product_service; GRANT SELECT,UPDATE ON product.evidence TO lites_product_service; GRANT SELECT,INSERT ON product.project_test_runs,product.artifact_revisions,product.artifact_revision_evidence,product.portfolio_export_artifacts,product.portfolio_export_evidence TO lites_product_service; GRANT SELECT,INSERT,UPDATE ON agent.runs,agent.jobs,agent.job_attempts,agent.inbox,agent.event_cursors,agent.outbox,agent.idempotency_responses TO lites_product_service; GRANT SELECT,INSERT ON agent.events TO lites_product_service; GRANT EXECUTE ON FUNCTION product.lock_project_completion_manifest(uuid,uuid,uuid,bigint,text), agent.list_recoverable_portfolio_tenants(uuid,uuid,integer,integer,integer,timestamptz,timestamptz), agent.list_expirable_portfolio_tenants(uuid,uuid,integer,integer,integer,timestamptz) TO lites_product_service;" >/dev/null

docker exec "${container_name}" psql -v ON_ERROR_STOP=1 -U postgres -d lites_foundation -c \
  "GRANT SELECT ON agent.behavior_snapshots,agent.behavior_channel_deployments TO lites_product_service;" >/dev/null

docker exec "${container_name}" psql -v ON_ERROR_STOP=1 -U postgres -d lites_foundation -c \
  "GRANT SELECT,INSERT,UPDATE ON agent.conversations TO lites_product_service; GRANT SELECT,INSERT ON agent.run_messages TO lites_product_service; GRANT EXECUTE ON FUNCTION agent.lock_owned_route_planning_mission(uuid,uuid,uuid) TO lites_product_service;" >/dev/null

docker exec "${container_name}" psql -v ON_ERROR_STOP=1 -U postgres -d lites_foundation -c \
  "GRANT EXECUTE ON FUNCTION agent.list_route_planner_reconciliation_tenants(uuid,integer) TO lites_product_service;" >/dev/null

docker exec "${container_name}" psql -v ON_ERROR_STOP=1 -U postgres -d lites_foundation -c \
  "GRANT SELECT ON product.rubric_versions TO lites_product_service; GRANT SELECT,INSERT,UPDATE ON product.user_preferences,product.reminder_schedules,product.reminder_deliveries,product.daily_tasks,product.daily_task_generations,product.submission_review_generations,product.project_test_generations TO lites_product_service; GRANT SELECT,INSERT ON product.submissions,product.reviews TO lites_product_service; GRANT SELECT,INSERT ON product.evidence TO lites_product_service; GRANT EXECUTE ON FUNCTION agent.lock_owned_daily_planning_mission(uuid,uuid,uuid,uuid,bigint), agent.list_daily_task_planner_reconciliation_tenants(uuid,integer), agent.lock_owned_submission_review(uuid,uuid,uuid,integer,uuid,bigint), agent.list_submission_review_reconciliation_tenants(uuid,integer), agent.list_due_reminder_schedule_tenants(uuid,integer), agent.lock_owned_project_evaluation(uuid,uuid,uuid,bigint,uuid,bigint,text), agent.list_project_test_reconciliation_tenants(uuid,integer), agent.lock_owned_portfolio_export(uuid,uuid,uuid,bigint,text) TO lites_product_service;" >/dev/null

docker exec "${container_name}" psql -v ON_ERROR_STOP=1 -U postgres -d lites_foundation -c \
  "GRANT SELECT ON agent.tool_calls,agent.tool_effects TO lites_product_service; GRANT EXECUTE ON FUNCTION agent.list_portfolio_export_finalization_tenants(uuid,uuid,integer) TO lites_product_service;" >/dev/null

docker exec "${container_name}" psql -v ON_ERROR_STOP=1 -U postgres -d lites_foundation -c \
  "GRANT SELECT ON product.aggregate_metrics,product.aggregate_snapshots TO lites_product_service; GRANT SELECT,INSERT ON product.aggregate_suppressions,product.aggregate_query_audit TO lites_product_service; GRANT SELECT,INSERT,UPDATE ON product.aggregate_query_budgets TO lites_product_service;" >/dev/null

docker exec "${container_name}" psql -v ON_ERROR_STOP=1 -U postgres -d lites_foundation -c \
  "GRANT SELECT ON identity.tenants TO lites_product_service; GRANT SELECT,INSERT,UPDATE ON product.programs,product.cohorts,product.enrollments TO lites_product_service; GRANT SELECT,INSERT ON product.role_packs TO lites_product_service; GRANT SELECT ON product.task_templates TO lites_product_service;" >/dev/null

docker exec "${container_name}" psql -v ON_ERROR_STOP=1 -U postgres -d lites_foundation -c \
  "CREATE ROLE lites_contract_service LOGIN PASSWORD 'foundation_contract_service' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS; GRANT CONNECT ON DATABASE lites_foundation TO lites_contract_service; GRANT USAGE ON SCHEMA identity,agent,contracts TO lites_contract_service; GRANT SELECT ON identity.tenants,identity.memberships,identity.sessions TO lites_contract_service; GRANT SELECT ON contracts.contracts,contracts.seat_allocations,contracts.contract_audit_events TO lites_contract_service; GRANT SELECT,INSERT ON contracts.contract_proposals,contracts.contract_approval_decisions TO lites_contract_service; GRANT SELECT,INSERT,UPDATE ON agent.idempotency_responses,agent.outbox TO lites_contract_service; GRANT SELECT,INSERT ON agent.events TO lites_contract_service; GRANT EXECUTE ON FUNCTION contracts.apply_approved_contract_proposal(uuid,uuid,uuid,uuid,timestamptz) TO lites_contract_service;" >/dev/null

docker exec "${container_name}" psql -v ON_ERROR_STOP=1 -U postgres -d lites_foundation -c \
  "CREATE ROLE lites_scheduler_service LOGIN PASSWORD 'foundation_scheduler_service' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS; GRANT CONNECT ON DATABASE lites_foundation TO lites_scheduler_service; GRANT USAGE ON SCHEMA agent TO lites_scheduler_service; GRANT SELECT,UPDATE ON agent.jobs TO lites_scheduler_service; GRANT EXECUTE ON FUNCTION agent.scheduler_claim_resource(text,text,bytea,timestamptz,timestamptz), agent.scheduler_list_ready_jobs(text,text,bigint,bytea,uuid,timestamptz,integer), agent.scheduler_list_ready_jobs_v2(text,text,bigint,bytea,uuid,timestamptz,integer), agent.scheduler_active_counts(text,integer), agent.scheduler_active_run_count(), agent.scheduler_commit_dispatch(text,text,bigint,bytea,jsonb,jsonb,timestamptz,timestamptz), agent.scheduler_abort_resource(text,text,bigint,bytea,timestamptz) TO lites_scheduler_service;" >/dev/null

docker exec "${container_name}" psql -v ON_ERROR_STOP=1 -U postgres -d lites_foundation -c \
  "CREATE ROLE lites_realtime_service LOGIN PASSWORD 'foundation_realtime_service' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS; GRANT CONNECT ON DATABASE lites_foundation TO lites_realtime_service; GRANT USAGE ON SCHEMA agent TO lites_realtime_service; GRANT SELECT ON agent.events,agent.event_cursors TO lites_realtime_service;" >/dev/null

docker exec "${container_name}" psql -v ON_ERROR_STOP=1 -U postgres -d lites_foundation -c \
  "CREATE ROLE lites_runtime_service LOGIN PASSWORD 'foundation_runtime_service' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS; GRANT CONNECT ON DATABASE lites_foundation TO lites_runtime_service; GRANT USAGE ON SCHEMA agent TO lites_runtime_service; GRANT SELECT ON agent.runs,agent.run_cancellations,agent.tool_calls,agent.events TO lites_runtime_service; GRANT SELECT(host_id,version,pool_key,status,architecture,availability_zone,firecracker_version,kernel_catalog_hash,rootfs_catalog_hash,scratch_template_digest,capacity_vcpu,capacity_memory_mib,capacity_disk_mib,capacity_sessions,allocated_vcpu,allocated_memory_mib,allocated_disk_mib,allocated_sessions,heartbeat_at,heartbeat_deadline,created_at,updated_at) ON agent.runtime_hosts TO lites_runtime_service; GRANT SELECT,INSERT ON agent.runtime_policy_snapshots TO lites_runtime_service; GRANT SELECT,INSERT,UPDATE ON agent.runtime_sessions,agent.runtime_allocations,agent.runtime_executions,agent.event_cursors,agent.outbox TO lites_runtime_service; GRANT INSERT ON agent.events TO lites_runtime_service; GRANT EXECUTE ON FUNCTION agent.list_runtime_recovery_tenants(uuid,uuid,integer,integer,integer,timestamptz),agent.runtime_register_host(text,text,text,text,text,text,text,bytea,integer,integer,integer,integer,timestamptz,timestamptz),agent.runtime_heartbeat_host(text,bigint,bytea,timestamptz,timestamptz),agent.runtime_set_host_status(text,bigint,bytea,text,timestamptz,timestamptz),agent.runtime_lock_execution_right(uuid,uuid,uuid,bigint,timestamptz),agent.runtime_lock_owned_machine(uuid,text,bytea,uuid,uuid,uuid,uuid,text,bigint),agent.runtime_lock_due_session(uuid,uuid,uuid,bigint,timestamptz),agent.runtime_lock_cancelled_run_session(uuid,uuid,uuid,uuid,uuid,bigint,timestamptz),agent.runtime_list_host_machines(uuid,text,bytea,uuid,integer),agent.runtime_active_session_count() TO lites_runtime_service;" >/dev/null

docker exec "${container_name}" psql -v ON_ERROR_STOP=1 -U postgres -d lites_foundation -c \
  "CREATE ROLE lites_runtime_sweeper_service LOGIN PASSWORD 'foundation_runtime_sweeper_service' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS; GRANT CONNECT ON DATABASE lites_foundation TO lites_runtime_sweeper_service; GRANT USAGE ON SCHEMA agent TO lites_runtime_sweeper_service; GRANT SELECT ON agent.runs,agent.run_cancellations,agent.events TO lites_runtime_sweeper_service; GRANT SELECT,INSERT,UPDATE ON agent.runtime_sessions,agent.runtime_allocations,agent.event_cursors,agent.outbox TO lites_runtime_sweeper_service; GRANT SELECT,UPDATE ON agent.runtime_executions TO lites_runtime_sweeper_service; GRANT INSERT ON agent.events TO lites_runtime_sweeper_service; GRANT EXECUTE ON FUNCTION agent.list_runtime_recovery_tenants(uuid,uuid,integer,integer,integer,timestamptz),agent.runtime_lock_due_session(uuid,uuid,uuid,bigint,timestamptz),agent.runtime_lock_cancelled_run_session(uuid,uuid,uuid,uuid,uuid,bigint,timestamptz) TO lites_runtime_sweeper_service;" >/dev/null

docker exec "${container_name}" psql -v ON_ERROR_STOP=1 -U postgres -d lites_foundation -c \
  "CREATE ROLE lites_behavior_service LOGIN PASSWORD 'foundation_behavior_service' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS; GRANT CONNECT ON DATABASE lites_foundation TO lites_behavior_service; GRANT USAGE ON SCHEMA agent,identity TO lites_behavior_service; GRANT SELECT,INSERT ON agent.behavior_snapshots,agent.behavior_evaluation_reports,agent.behavior_channel_deployments,agent.events TO lites_behavior_service; GRANT SELECT,INSERT,UPDATE ON agent.event_cursors,agent.outbox,agent.idempotency_responses TO lites_behavior_service; GRANT SELECT ON identity.sessions,identity.memberships TO lites_behavior_service;" >/dev/null

container_port="$(docker port "${container_name}" 5432/tcp | head -n 1 | sed 's/.*://')"

export LITES_TEST_ADMIN_DATABASE_URL="postgres://postgres:foundation_admin@127.0.0.1:${container_port}/lites_foundation?sslmode=disable"
export LITES_TEST_IDENTITY_DATABASE_URL="postgres://lites_identity_service:foundation_service@127.0.0.1:${container_port}/lites_foundation?sslmode=disable"
export LITES_TEST_AGENT_DATABASE_URL="postgres://lites_agent_service:foundation_agent_service@127.0.0.1:${container_port}/lites_foundation?sslmode=disable"
export LITES_TEST_PRODUCT_DATABASE_URL="postgres://lites_product_service:foundation_product_service@127.0.0.1:${container_port}/lites_foundation?sslmode=disable"
export LITES_TEST_CONTRACT_DATABASE_URL="postgres://lites_contract_service:foundation_contract_service@127.0.0.1:${container_port}/lites_foundation?sslmode=disable"
export LITES_TEST_SCHEDULER_DATABASE_URL="postgres://lites_scheduler_service:foundation_scheduler_service@127.0.0.1:${container_port}/lites_foundation?sslmode=disable"
export LITES_TEST_REALTIME_DATABASE_URL="postgres://lites_realtime_service:foundation_realtime_service@127.0.0.1:${container_port}/lites_foundation?sslmode=disable"
export LITES_TEST_RUNTIME_DATABASE_URL="postgres://lites_runtime_service:foundation_runtime_service@127.0.0.1:${container_port}/lites_foundation?sslmode=disable"
export LITES_TEST_RUNTIME_SWEEPER_DATABASE_URL="postgres://lites_runtime_sweeper_service:foundation_runtime_sweeper_service@127.0.0.1:${container_port}/lites_foundation?sslmode=disable"
export LITES_TEST_BEHAVIOR_DATABASE_URL="postgres://lites_behavior_service:foundation_behavior_service@127.0.0.1:${container_port}/lites_foundation?sslmode=disable"
export GOCACHE="${go_cache}" GOMODCACHE="${go_mod_cache}" GOTMPDIR="${go_tmp}"
test_run_args=(-run "${LITES_FOUNDATION_TEST_RUN:-.}")
if [[ -n "${LITES_FOUNDATION_TEST_PACKAGES:-}" ]]; then
  read -r -a test_packages <<<"${LITES_FOUNDATION_TEST_PACKAGES}"
else
  test_packages=(./internal/identity/postgres ./internal/identity/mail ./internal/eventbus/natsjs ./internal/eventstore/postgres ./internal/execution/postgres ./internal/product/postgres ./internal/contracts/postgres ./internal/billing/postgres ./internal/behavior/postgres ./internal/agentworker ./internal/toolworker ./internal/toolreconciler ./internal/llmgateway/postgres ./internal/memory/postgres ./internal/realtime/postgres ./internal/runtime ./internal/runtime/sessionrequest ./internal/runtime/postgres ./internal/runtime/sweeper)
fi
if [[ -n "${LITES_FOUNDATION_TEST_JSON:-}" ]]; then
  if [[ "${LITES_FOUNDATION_TEST_QUIET:-false}" == "true" ]]; then
    go test -p=1 -json -count=1 -timeout=60s -tags=integration "${test_run_args[@]}" "${test_packages[@]}" >"${LITES_FOUNDATION_TEST_JSON}"
  else
    go test -p=1 -json -count=1 -timeout=60s -tags=integration "${test_run_args[@]}" "${test_packages[@]}" | tee "${LITES_FOUNDATION_TEST_JSON}"
  fi
else
  go test -p=1 -count=1 -timeout=60s -tags=integration "${test_run_args[@]}" "${test_packages[@]}"
fi
