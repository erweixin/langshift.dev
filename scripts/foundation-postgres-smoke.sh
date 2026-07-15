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
  deploy/migrations/000015_repair_recovery_scan.up.sql \
  deploy/migrations/000016_generalized_repair_targets.up.sql \
  deploy/migrations/000017_repair_evidence_expiry.up.sql \
  deploy/migrations/000018_approval_control_plane.up.sql \
  deploy/migrations/000019_approval_scope_drift.up.sql \
  deploy/migrations/000020_workspace_revision_protocol.up.sql \
  deploy/migrations/000021_workspace_prepared_proposal.up.sql \
  deploy/migrations/000022_workspace_publish_heartbeat.up.sql \
  deploy/migrations/000023_workspace_reconciliation_lifecycle.up.sql \
  deploy/migrations/000024_artifact_revision_protocol.up.sql \
  deploy/migrations/000025_portfolio_export_protocol.up.sql \
  deploy/migrations/000026_llm_provider_attempt_protocol.up.sql \
  deploy/migrations/000027_usage_accounting_protocol.up.sql \
  deploy/migrations/000028_memory_retrieval_protocol.up.sql \
  deploy/migrations/000029_runtime_session_protocol.up.sql \
  deploy/migrations/000030_runtime_host_capacity_protocol.up.sql \
  deploy/migrations/000031_runtime_execution_right_lock.up.sql \
  deploy/migrations/000032_runtime_hostless_termination.up.sql \
  deploy/migrations/000033_runtime_epoch_authorization.up.sql \
  deploy/migrations/000034_runtime_boot_receipt.up.sql \
  deploy/migrations/000035_runtime_host_recovery_authority.up.sql \
  deploy/migrations/000036_runtime_host_restart_recovery.up.sql \
  deploy/migrations/000037_behavior_snapshot_promotion.up.sql \
  deploy/migrations/000038_run_behavior_binding.up.sql \
  deploy/migrations/000039_idempotency_prepared_event_payload.up.sql \
  deploy/migrations/000040_run_cancellation_barrier.up.sql \
  deploy/migrations/000041_run_cancellation_recovery_discovery.up.sql \
  deploy/migrations/000042_runtime_run_cancellation_authority.up.sql \
  deploy/migrations/000043_child_run_orchestration_contract.up.sql \
  deploy/migrations/000044_run_cancellation_propagation.up.sql \
  deploy/migrations/000045_child_group_remainder_cancellation.up.sql \
  deploy/migrations/000046_scheduler_capacity_and_dispatch_fence.up.sql \
  deploy/migrations/000047_run_context_source_protocol.up.sql \
  deploy/migrations/000048_llm_context_prompt_binding.up.sql \
  deploy/migrations/000049_direct_tool_approval_protocol.up.sql \
  deploy/migrations/000050_tool_execution_overlay.up.sql; do
  target="/tmp/$(basename "${migration}")"
  docker cp "${migration}" "${container_name}:${target}" >/dev/null
  docker exec "${container_name}" psql -v ON_ERROR_STOP=1 -U postgres -d lites_foundation -f "${target}" >/dev/null
done

docker exec "${container_name}" psql -v ON_ERROR_STOP=1 -U postgres -d lites_foundation -c \
  "CREATE ROLE lites_identity_service LOGIN PASSWORD 'foundation_service' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS; GRANT CONNECT ON DATABASE lites_foundation TO lites_identity_service; GRANT USAGE ON SCHEMA identity, product, agent TO lites_identity_service; GRANT SELECT, INSERT, UPDATE ON identity.users, identity.password_credentials, identity.email_verifications, identity.password_reset_requests, identity.sessions, identity.memberships, identity.membership_imports, identity.account_erasure_requests, identity.invitations, identity.invitation_imports, identity.anonymous_subjects, identity.onboarding_sessions, identity.onboarding_claims TO lites_identity_service; GRANT SELECT, INSERT ON identity.anonymous_erasure_receipts TO lites_identity_service; GRANT SELECT, INSERT ON identity.tenants TO lites_identity_service; GRANT INSERT ON identity.security_events TO lites_identity_service; GRANT EXECUTE ON FUNCTION identity.lookup_invitation_for_acceptance(text,bytea), identity.lock_active_tenant(uuid), agent.list_ready_outbox_tenants(uuid,uuid,integer,integer,integer) TO lites_identity_service; GRANT SELECT ON product.role_profiles TO lites_identity_service; GRANT SELECT, INSERT, UPDATE, DELETE ON product.missions, product.route_revisions TO lites_identity_service; GRANT SELECT, INSERT ON product.mission_imports TO lites_identity_service; GRANT SELECT, INSERT, UPDATE ON product.data_export_requests TO lites_identity_service; GRANT SELECT, INSERT, UPDATE ON agent.idempotency_responses, agent.event_cursors, agent.outbox, agent.inbox, agent.repair_commands TO lites_identity_service; GRANT DELETE ON agent.idempotency_responses TO lites_identity_service; GRANT SELECT, INSERT ON agent.events, agent.repair_approvals TO lites_identity_service;" >/dev/null

docker exec "${container_name}" psql -v ON_ERROR_STOP=1 -U postgres -d lites_foundation -c \
  "CREATE ROLE lites_agent_service LOGIN PASSWORD 'foundation_agent_service' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS; GRANT CONNECT ON DATABASE lites_foundation TO lites_agent_service; GRANT USAGE ON SCHEMA identity, product, agent, contracts TO lites_agent_service; GRANT SELECT ON identity.sessions, identity.memberships, product.byok_credentials, product.byok_credential_versions TO lites_agent_service; GRANT SELECT, INSERT, UPDATE ON agent.runs, agent.run_cancellations, agent.child_group_cancellations, agent.jobs, agent.job_attempts, agent.inbox, agent.event_cursors, agent.outbox, agent.idempotency_responses, agent.tool_calls, agent.tool_effects, agent.parallel_groups, agent.parallel_group_members, agent.child_groups, agent.child_group_members, agent.orchestration_quotas, agent.continuations, agent.repair_commands, agent.approvals, agent.workspace_revision_commits, agent.llm_attempts, agent.llm_provider_attempts TO lites_agent_service; GRANT SELECT,INSERT ON agent.run_messages,agent.tool_proposals TO lites_agent_service; GRANT SELECT ON agent.runtime_sessions TO lites_agent_service; GRANT SELECT, INSERT ON agent.events, agent.repair_approvals, agent.approval_decisions TO lites_agent_service; GRANT SELECT ON contracts.credit_buckets TO lites_agent_service; GRANT SELECT,INSERT,UPDATE ON contracts.usage_reservations TO lites_agent_service; GRANT SELECT,INSERT ON contracts.usage_ledger,contracts.provider_costs TO lites_agent_service; GRANT EXECUTE ON FUNCTION agent.list_command_reconciliation_tenants(uuid,uuid,integer,integer,integer,timestamptz,timestamptz), agent.list_expired_tool_effect_tenants(uuid,uuid,integer,integer,integer,timestamptz), agent.list_recoverable_repair_tenants(uuid,uuid,integer,integer,integer,timestamptz), agent.list_expired_repair_tenants(uuid,uuid,integer,integer,integer,timestamptz), agent.list_expired_approval_tenants(uuid,uuid,integer,integer,integer,timestamptz), agent.list_stale_approval_tenants(uuid,uuid,integer,integer,integer), agent.list_workspace_recovery_tenants(uuid,uuid,integer,integer,integer,timestamptz), agent.list_recoverable_llm_provider_tenants(uuid,uuid,integer,integer,integer,timestamptz), agent.list_run_cancellation_tenants(uuid,uuid,integer,integer,integer,timestamptz), contracts.list_releasable_usage_tenants(uuid,uuid,integer,integer,integer,timestamptz), contracts.reserve_credit_units(uuid,uuid,bigint,timestamptz,timestamptz), contracts.settle_credit_units(uuid,uuid,bigint,bigint,timestamptz), contracts.release_credit_units(uuid,uuid,bigint,timestamptz) TO lites_agent_service;" >/dev/null

docker exec "${container_name}" psql -v ON_ERROR_STOP=1 -U postgres -d lites_foundation -c \
  "GRANT SELECT ON product.memory_policies,product.projects,product.share_grants TO lites_agent_service; GRANT SELECT,INSERT,UPDATE ON agent.memory_documents,agent.memory_index_projections TO lites_agent_service; GRANT SELECT,INSERT ON agent.memory_document_revisions,agent.memory_revision_sources,agent.memory_revision_subjects,agent.memory_revision_derivations,agent.memory_tombstones,agent.retrieval_manifests,agent.retrieval_manifest_chunks,agent.memory_accesses TO lites_agent_service;" >/dev/null

docker exec "${container_name}" psql -v ON_ERROR_STOP=1 -U postgres -d lites_foundation -c \
  "GRANT SELECT ON agent.behavior_snapshots,agent.behavior_channel_deployments TO lites_agent_service;" >/dev/null

docker exec "${container_name}" psql -v ON_ERROR_STOP=1 -U postgres -d lites_foundation -c \
  "GRANT SELECT ON agent.platform_tool_execution_overlays,agent.tenant_tool_execution_overlays TO lites_agent_service;" >/dev/null

docker exec "${container_name}" psql -v ON_ERROR_STOP=1 -U postgres -d lites_foundation -c \
  "CREATE ROLE lites_product_service LOGIN PASSWORD 'foundation_product_service' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS; GRANT CONNECT ON DATABASE lites_foundation TO lites_product_service; GRANT USAGE ON SCHEMA product,agent TO lites_product_service; GRANT SELECT,UPDATE ON product.projects,product.evidence TO lites_product_service; GRANT SELECT ON product.project_workspace_bindings TO lites_product_service; GRANT SELECT,INSERT,UPDATE ON product.artifacts,product.portfolio_exports TO lites_product_service; GRANT SELECT,INSERT ON product.artifact_revisions,product.artifact_revision_evidence,product.portfolio_export_artifacts,product.portfolio_export_evidence TO lites_product_service; GRANT SELECT,INSERT,UPDATE ON agent.runs,agent.jobs,agent.job_attempts,agent.inbox,agent.event_cursors,agent.outbox TO lites_product_service; GRANT SELECT,INSERT ON agent.events TO lites_product_service; GRANT EXECUTE ON FUNCTION agent.list_recoverable_portfolio_tenants(uuid,uuid,integer,integer,integer,timestamptz,timestamptz), agent.list_expirable_portfolio_tenants(uuid,uuid,integer,integer,integer,timestamptz) TO lites_product_service;" >/dev/null

docker exec "${container_name}" psql -v ON_ERROR_STOP=1 -U postgres -d lites_foundation -c \
  "GRANT SELECT ON agent.behavior_snapshots,agent.behavior_channel_deployments TO lites_product_service;" >/dev/null

docker exec "${container_name}" psql -v ON_ERROR_STOP=1 -U postgres -d lites_foundation -c \
  "CREATE ROLE lites_scheduler_service LOGIN PASSWORD 'foundation_scheduler_service' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS; GRANT CONNECT ON DATABASE lites_foundation TO lites_scheduler_service; GRANT USAGE ON SCHEMA agent TO lites_scheduler_service; GRANT SELECT,UPDATE ON agent.jobs TO lites_scheduler_service; GRANT EXECUTE ON FUNCTION agent.scheduler_claim_resource(text,text,bytea,timestamptz,timestamptz), agent.scheduler_list_ready_jobs(text,text,bigint,bytea,uuid,timestamptz,integer), agent.scheduler_list_ready_jobs_v2(text,text,bigint,bytea,uuid,timestamptz,integer), agent.scheduler_active_counts(text,integer), agent.scheduler_commit_dispatch(text,text,bigint,bytea,jsonb,jsonb,timestamptz,timestamptz), agent.scheduler_abort_resource(text,text,bigint,bytea,timestamptz) TO lites_scheduler_service;" >/dev/null

docker exec "${container_name}" psql -v ON_ERROR_STOP=1 -U postgres -d lites_foundation -c \
  "CREATE ROLE lites_realtime_service LOGIN PASSWORD 'foundation_realtime_service' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS; GRANT CONNECT ON DATABASE lites_foundation TO lites_realtime_service; GRANT USAGE ON SCHEMA agent TO lites_realtime_service; GRANT SELECT ON agent.events,agent.event_cursors TO lites_realtime_service;" >/dev/null

docker exec "${container_name}" psql -v ON_ERROR_STOP=1 -U postgres -d lites_foundation -c \
  "CREATE ROLE lites_runtime_service LOGIN PASSWORD 'foundation_runtime_service' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS; GRANT CONNECT ON DATABASE lites_foundation TO lites_runtime_service; GRANT USAGE ON SCHEMA agent TO lites_runtime_service; GRANT SELECT ON agent.runs,agent.run_cancellations,agent.tool_calls,agent.events TO lites_runtime_service; GRANT SELECT(host_id,version,pool_key,status,architecture,availability_zone,firecracker_version,kernel_catalog_hash,rootfs_catalog_hash,scratch_template_digest,capacity_vcpu,capacity_memory_mib,capacity_disk_mib,capacity_sessions,allocated_vcpu,allocated_memory_mib,allocated_disk_mib,allocated_sessions,heartbeat_at,heartbeat_deadline,created_at,updated_at) ON agent.runtime_hosts TO lites_runtime_service; GRANT SELECT,INSERT ON agent.runtime_policy_snapshots TO lites_runtime_service; GRANT SELECT,INSERT,UPDATE ON agent.runtime_sessions,agent.runtime_allocations,agent.event_cursors,agent.outbox TO lites_runtime_service; GRANT INSERT ON agent.events TO lites_runtime_service; GRANT EXECUTE ON FUNCTION agent.list_runtime_recovery_tenants(uuid,uuid,integer,integer,integer,timestamptz),agent.runtime_register_host(text,text,text,text,text,text,text,bytea,integer,integer,integer,integer,timestamptz,timestamptz),agent.runtime_heartbeat_host(text,bigint,bytea,timestamptz,timestamptz),agent.runtime_set_host_status(text,bigint,bytea,text,timestamptz,timestamptz),agent.runtime_lock_execution_right(uuid,uuid,uuid,bigint,timestamptz),agent.runtime_lock_owned_machine(uuid,text,bytea,uuid,uuid,uuid,uuid,text,bigint),agent.runtime_lock_due_session(uuid,uuid,uuid,bigint,timestamptz),agent.runtime_lock_cancelled_run_session(uuid,uuid,uuid,uuid,uuid,bigint,timestamptz),agent.runtime_list_host_machines(uuid,text,bytea,uuid,integer) TO lites_runtime_service;" >/dev/null

docker exec "${container_name}" psql -v ON_ERROR_STOP=1 -U postgres -d lites_foundation -c \
  "CREATE ROLE lites_runtime_sweeper_service LOGIN PASSWORD 'foundation_runtime_sweeper_service' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS; GRANT CONNECT ON DATABASE lites_foundation TO lites_runtime_sweeper_service; GRANT USAGE ON SCHEMA agent TO lites_runtime_sweeper_service; GRANT SELECT ON agent.runs,agent.run_cancellations,agent.events TO lites_runtime_sweeper_service; GRANT SELECT,INSERT,UPDATE ON agent.runtime_sessions,agent.runtime_allocations,agent.event_cursors,agent.outbox TO lites_runtime_sweeper_service; GRANT INSERT ON agent.events TO lites_runtime_sweeper_service; GRANT EXECUTE ON FUNCTION agent.list_runtime_recovery_tenants(uuid,uuid,integer,integer,integer,timestamptz),agent.runtime_lock_due_session(uuid,uuid,uuid,bigint,timestamptz),agent.runtime_lock_cancelled_run_session(uuid,uuid,uuid,uuid,uuid,bigint,timestamptz) TO lites_runtime_sweeper_service;" >/dev/null

docker exec "${container_name}" psql -v ON_ERROR_STOP=1 -U postgres -d lites_foundation -c \
  "CREATE ROLE lites_behavior_service LOGIN PASSWORD 'foundation_behavior_service' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS; GRANT CONNECT ON DATABASE lites_foundation TO lites_behavior_service; GRANT USAGE ON SCHEMA agent,identity TO lites_behavior_service; GRANT SELECT,INSERT ON agent.behavior_snapshots,agent.behavior_evaluation_reports,agent.behavior_channel_deployments,agent.events TO lites_behavior_service; GRANT SELECT,INSERT,UPDATE ON agent.event_cursors,agent.outbox,agent.idempotency_responses TO lites_behavior_service; GRANT SELECT ON identity.sessions,identity.memberships TO lites_behavior_service;" >/dev/null

container_port="$(docker port "${container_name}" 5432/tcp | head -n 1 | sed 's/.*://')"

export LITES_TEST_ADMIN_DATABASE_URL="postgres://postgres:foundation_admin@127.0.0.1:${container_port}/lites_foundation?sslmode=disable"
export LITES_TEST_IDENTITY_DATABASE_URL="postgres://lites_identity_service:foundation_service@127.0.0.1:${container_port}/lites_foundation?sslmode=disable"
export LITES_TEST_AGENT_DATABASE_URL="postgres://lites_agent_service:foundation_agent_service@127.0.0.1:${container_port}/lites_foundation?sslmode=disable"
export LITES_TEST_PRODUCT_DATABASE_URL="postgres://lites_product_service:foundation_product_service@127.0.0.1:${container_port}/lites_foundation?sslmode=disable"
export LITES_TEST_SCHEDULER_DATABASE_URL="postgres://lites_scheduler_service:foundation_scheduler_service@127.0.0.1:${container_port}/lites_foundation?sslmode=disable"
export LITES_TEST_REALTIME_DATABASE_URL="postgres://lites_realtime_service:foundation_realtime_service@127.0.0.1:${container_port}/lites_foundation?sslmode=disable"
export LITES_TEST_RUNTIME_DATABASE_URL="postgres://lites_runtime_service:foundation_runtime_service@127.0.0.1:${container_port}/lites_foundation?sslmode=disable"
export LITES_TEST_RUNTIME_SWEEPER_DATABASE_URL="postgres://lites_runtime_sweeper_service:foundation_runtime_sweeper_service@127.0.0.1:${container_port}/lites_foundation?sslmode=disable"
export LITES_TEST_BEHAVIOR_DATABASE_URL="postgres://lites_behavior_service:foundation_behavior_service@127.0.0.1:${container_port}/lites_foundation?sslmode=disable"
export GOCACHE="${go_cache}" GOMODCACHE="${go_mod_cache}" GOTMPDIR="${go_tmp}"
if [[ -n "${LITES_FOUNDATION_TEST_JSON:-}" ]]; then
  go test -p=1 -json -count=1 -timeout=60s -tags=integration ./internal/identity/postgres ./internal/identity/mail ./internal/eventstore/postgres ./internal/execution/postgres ./internal/product/postgres ./internal/billing/postgres ./internal/behavior/postgres ./internal/agentworker ./internal/toolworker ./internal/llmgateway/postgres ./internal/memory/postgres ./internal/realtime/postgres ./internal/runtime ./internal/runtime/postgres ./internal/runtime/sweeper | tee "${LITES_FOUNDATION_TEST_JSON}"
else
  go test -p=1 -count=1 -timeout=60s -tags=integration ./internal/identity/postgres ./internal/identity/mail ./internal/eventstore/postgres ./internal/execution/postgres ./internal/product/postgres ./internal/billing/postgres ./internal/behavior/postgres ./internal/agentworker ./internal/toolworker ./internal/llmgateway/postgres ./internal/memory/postgres ./internal/realtime/postgres ./internal/runtime ./internal/runtime/postgres ./internal/runtime/sweeper
fi
