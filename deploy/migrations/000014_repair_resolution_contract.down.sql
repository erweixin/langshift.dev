DROP INDEX IF EXISTS agent.repair_commands_pending_idx;
DROP TRIGGER IF EXISTS repair_commands_lifecycle ON agent.repair_commands;
DROP FUNCTION IF EXISTS agent.enforce_repair_command_lifecycle();

ALTER TABLE agent.repair_approvals
  DROP CONSTRAINT IF EXISTS repair_approvals_event_unique,
  DROP CONSTRAINT IF EXISTS repair_approvals_event_fk,
  DROP CONSTRAINT IF EXISTS repair_approvals_tenant_repair_fk,
  DROP CONSTRAINT IF EXISTS repair_approvals_session_fk,
  DROP CONSTRAINT IF EXISTS repair_approvals_approver_fk,
  DROP CONSTRAINT IF EXISTS repair_approvals_scope_contract,
  DROP CONSTRAINT IF EXISTS repair_approvals_decision_contract,
  DROP COLUMN IF EXISTS approval_event_id,
  DROP COLUMN IF EXISTS effect_version,
  DROP COLUMN IF EXISTS decision;

ALTER TABLE agent.repair_approvals
  ADD CONSTRAINT repair_approvals_repair_command_id_fk FOREIGN KEY (repair_command_id)
    REFERENCES agent.repair_commands(id) ON DELETE CASCADE;

ALTER TABLE agent.repair_commands
  DROP CONSTRAINT IF EXISTS repair_commands_target_tool_fk,
  DROP CONSTRAINT IF EXISTS repair_commands_initiator_fk,
  DROP CONSTRAINT IF EXISTS repair_commands_lifecycle_contract,
  DROP CONSTRAINT IF EXISTS repair_commands_scope_contract,
  DROP CONSTRAINT IF EXISTS repair_commands_status_contract,
  DROP CONSTRAINT IF EXISTS repair_commands_resolution_contract,
  DROP CONSTRAINT IF EXISTS repair_commands_kind_contract,
  DROP CONSTRAINT IF EXISTS repair_commands_tenant_id_id_unique,
  DROP COLUMN IF EXISTS rejected_at,
  DROP COLUMN IF EXISTS approved_at,
  DROP COLUMN IF EXISTS residual_risk_ref,
  DROP COLUMN IF EXISTS effect_key,
  DROP COLUMN IF EXISTS effect_version,
  DROP COLUMN IF EXISTS resolution;
