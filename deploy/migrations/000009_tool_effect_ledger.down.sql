DROP TRIGGER IF EXISTS tool_effects_lifecycle ON agent.tool_effects;
DROP FUNCTION IF EXISTS agent.enforce_tool_effect_lifecycle();

ALTER TABLE agent.tool_effects
  DROP CONSTRAINT IF EXISTS tool_effects_result_event_fk,
  DROP CONSTRAINT IF EXISTS tool_effects_execution_attempt_fk,
  DROP CONSTRAINT IF EXISTS tool_effects_tenant_tool_run_fk,
  DROP CONSTRAINT IF EXISTS tool_effects_tool_call_once,
  DROP CONSTRAINT IF EXISTS tool_effects_shape_contract,
  DROP CONSTRAINT IF EXISTS tool_effects_execution_owner_contract,
  DROP CONSTRAINT IF EXISTS tool_effects_identity_contract,
  ALTER COLUMN effect_class DROP NOT NULL,
  ADD CONSTRAINT tool_effects_result_event_fk FOREIGN KEY (result_event_id)
    REFERENCES agent.events(id) ON DELETE RESTRICT;

ALTER TABLE agent.tool_calls
  DROP CONSTRAINT IF EXISTS tool_calls_tenant_id_id_run_unique;

ALTER TABLE agent.job_attempts
  DROP CONSTRAINT IF EXISTS job_attempts_tenant_id_id_unique;
