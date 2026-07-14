DROP INDEX IF EXISTS agent.tool_effects_reconcile_idx;
DROP INDEX IF EXISTS agent.approvals_expiry_idx;
DROP INDEX IF EXISTS agent.job_attempts_expired_lease_idx;
DROP INDEX IF EXISTS agent.runs_sweeper_due_idx;

ALTER TABLE agent.tool_effects
  DROP CONSTRAINT IF EXISTS tool_effects_result_event_fk,
  DROP CONSTRAINT IF EXISTS tool_effects_tenant_tool_call_fk,
  DROP CONSTRAINT IF EXISTS tool_effects_tenant_run_fk,
  DROP CONSTRAINT IF EXISTS tool_effects_result_contract,
  DROP CONSTRAINT IF EXISTS tool_effects_status_contract,
  DROP COLUMN IF EXISTS result_event_id,
  DROP COLUMN IF EXISTS provider_request_id,
  DROP COLUMN IF EXISTS run_id,
  DROP COLUMN IF EXISTS tool_call_id;

ALTER TABLE agent.workspace_revision_commits
  DROP CONSTRAINT IF EXISTS workspace_revision_tenant_tool_call_fk,
  DROP CONSTRAINT IF EXISTS workspace_revision_confirmation_contract,
  DROP CONSTRAINT IF EXISTS workspace_revision_status_contract;

ALTER TABLE agent.approvals
  DROP CONSTRAINT IF EXISTS approvals_tenant_tool_call_fk,
  DROP CONSTRAINT IF EXISTS approvals_tenant_run_fk,
  DROP CONSTRAINT IF EXISTS approvals_target_contract,
  DROP CONSTRAINT IF EXISTS approvals_status_contract;

ALTER TABLE agent.job_attempts
  DROP CONSTRAINT IF EXISTS job_attempts_job_fence_unique,
  DROP CONSTRAINT IF EXISTS job_attempts_lifecycle_contract,
  DROP CONSTRAINT IF EXISTS job_attempts_status_contract;

DROP TRIGGER IF EXISTS job_attempts_append_only ON agent.job_attempts;
DROP FUNCTION IF EXISTS agent.enforce_job_attempt_lifecycle();

ALTER TABLE agent.tool_calls
  DROP CONSTRAINT IF EXISTS tool_calls_tenant_run_fk,
  DROP CONSTRAINT IF EXISTS tool_calls_tenant_id_id_unique,
  DROP CONSTRAINT IF EXISTS tool_calls_execution_right_contract,
  DROP CONSTRAINT IF EXISTS tool_calls_effect_key_contract,
  DROP CONSTRAINT IF EXISTS tool_calls_effect_class_contract,
  DROP CONSTRAINT IF EXISTS tool_calls_versions_contract,
  DROP CONSTRAINT IF EXISTS tool_calls_status_contract;

ALTER TABLE agent.runs
  DROP CONSTRAINT IF EXISTS runs_tenant_id_id_unique,
  DROP CONSTRAINT IF EXISTS runs_execution_right_contract,
  DROP CONSTRAINT IF EXISTS runs_versions_contract,
  DROP CONSTRAINT IF EXISTS runs_status_contract;

CREATE TRIGGER job_attempts_append_only BEFORE UPDATE OR DELETE ON agent.job_attempts FOR EACH ROW EXECUTE FUNCTION agent.reject_append_only_mutation();
