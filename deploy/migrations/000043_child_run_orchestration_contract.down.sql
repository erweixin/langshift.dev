DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM agent.runs WHERE parent_run_id IS NOT NULL)
    OR EXISTS (SELECT 1 FROM agent.child_groups)
    OR EXISTS (SELECT 1 FROM agent.orchestration_quotas) THEN
    RAISE EXCEPTION 'child Run orchestration facts prevent migration rollback';
  END IF;
END
$$;

DROP INDEX IF EXISTS agent.child_group_members_run_idx;
DROP INDEX IF EXISTS agent.runs_root_tree_idx;
DROP INDEX IF EXISTS agent.runs_parent_active_idx;
DROP TRIGGER IF EXISTS child_group_members_membership_complete ON agent.child_group_members;
DROP TRIGGER IF EXISTS child_groups_membership_complete ON agent.child_groups;
DROP FUNCTION IF EXISTS agent.verify_child_group_membership();
DROP TRIGGER IF EXISTS runs_orchestration_identity_immutable ON agent.runs;
DROP FUNCTION IF EXISTS agent.enforce_run_orchestration_identity();
DROP TRIGGER IF EXISTS runs_initialize_root_orchestration_identity ON agent.runs;
DROP FUNCTION IF EXISTS agent.initialize_root_run_orchestration_identity();
DROP TABLE IF EXISTS agent.orchestration_quotas;
DROP TABLE IF EXISTS agent.child_group_members;

ALTER TABLE agent.runs
  DROP CONSTRAINT IF EXISTS runs_child_group_fk;

ALTER TABLE agent.child_groups
  DROP CONSTRAINT IF EXISTS child_groups_joined_event_fk,
  DROP CONSTRAINT IF EXISTS child_groups_continuation_fk,
  DROP CONSTRAINT IF EXISTS child_groups_step_unique,
  DROP CONSTRAINT IF EXISTS child_groups_joined_contract,
  DROP CONSTRAINT IF EXISTS child_groups_continuation_contract,
  DROP CONSTRAINT IF EXISTS child_groups_step_contract,
  DROP CONSTRAINT IF EXISTS child_groups_join_contract,
  DROP CONSTRAINT IF EXISTS child_groups_parent_fk,
  DROP CONSTRAINT IF EXISTS child_groups_tenant_id_id_unique,
  DROP COLUMN IF EXISTS joined_event_id,
  DROP COLUMN IF EXISTS continuation_kind,
  DROP COLUMN IF EXISTS quorum_count,
  DROP COLUMN IF EXISTS step_id,
  ADD CONSTRAINT child_groups_parent_run_id_fk FOREIGN KEY (parent_run_id)
    REFERENCES agent.runs(id) ON DELETE CASCADE;

ALTER TABLE agent.tool_calls
  DROP CONSTRAINT tool_calls_execution_mode_contract,
  ADD CONSTRAINT tool_calls_execution_mode_contract CHECK (
    execution_mode IN ('worker_runtime','inline_platform')
    AND (execution_mode<>'inline_platform' OR (tool_name='memory_write' AND effect_class='idempotent_write'))
  );

ALTER TABLE agent.runs
  DROP CONSTRAINT IF EXISTS runs_spawn_tool_fk,
  DROP CONSTRAINT IF EXISTS runs_root_fk,
  DROP CONSTRAINT IF EXISTS runs_parent_fk,
  DROP CONSTRAINT IF EXISTS runs_result_summary_contract,
  DROP CONSTRAINT IF EXISTS runs_orchestration_identity_contract,
  DROP COLUMN IF EXISTS result_summary_hash,
  DROP COLUMN IF EXISTS result_summary_ref,
  DROP COLUMN IF EXISTS inherited_budget_microunits,
  DROP COLUMN IF EXISTS depth,
  DROP COLUMN IF EXISTS child_group_id,
  DROP COLUMN IF EXISTS spawn_tool_call_id,
  DROP COLUMN IF EXISTS root_run_id,
  DROP COLUMN IF EXISTS parent_run_id;
