ALTER TABLE agent.parallel_groups
  DROP CONSTRAINT IF EXISTS parallel_groups_continuation_fk;

ALTER TABLE agent.continuations
  DROP CONSTRAINT IF EXISTS continuations_command_fk,
  DROP CONSTRAINT IF EXISTS continuations_tenant_run_fk,
  DROP CONSTRAINT IF EXISTS continuations_group_once,
  DROP CONSTRAINT IF EXISTS continuations_tenant_id_id_unique,
  DROP CONSTRAINT IF EXISTS continuations_status_contract,
  DROP CONSTRAINT IF EXISTS continuations_kind_contract,
  DROP CONSTRAINT IF EXISTS continuations_group_kind_contract,
  DROP CONSTRAINT IF EXISTS continuations_version_contract,
  ADD CONSTRAINT continuations_tenant_id_run_id_run_version_group_kind_group_key
    UNIQUE (tenant_id,run_id,run_version,group_kind,group_id),
  ALTER COLUMN continuation_kind DROP NOT NULL;

ALTER TABLE agent.parallel_group_members
  DROP CONSTRAINT IF EXISTS parallel_group_members_tool_once;

ALTER TABLE agent.parallel_groups
  DROP CONSTRAINT IF EXISTS parallel_groups_joined_contract,
  DROP CONSTRAINT IF EXISTS parallel_groups_tenant_run_fk;

DROP INDEX IF EXISTS agent.tool_calls_expired_lease_idx;

ALTER TABLE agent.tool_calls
  DROP CONSTRAINT IF EXISTS tool_calls_result_event_fk,
  DROP CONSTRAINT IF EXISTS tool_calls_active_attempt_command_fk,
  DROP CONSTRAINT IF EXISTS tool_calls_active_command_fk,
  DROP CONSTRAINT IF EXISTS tool_calls_pending_command_fk;
