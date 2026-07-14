DROP INDEX IF EXISTS agent.parallel_group_members_tool_idx;
DROP TRIGGER IF EXISTS parallel_group_members_membership_complete ON agent.parallel_group_members;
DROP TRIGGER IF EXISTS parallel_groups_membership_complete ON agent.parallel_groups;
DROP FUNCTION IF EXISTS agent.verify_parallel_group_membership();
DROP TABLE IF EXISTS agent.parallel_group_members;

ALTER TABLE agent.parallel_groups
  DROP CONSTRAINT IF EXISTS parallel_groups_step_unique,
  DROP CONSTRAINT IF EXISTS parallel_groups_tenant_id_id_unique,
  DROP CONSTRAINT IF EXISTS parallel_groups_step_contract,
  DROP CONSTRAINT IF EXISTS parallel_groups_continuation_contract,
  DROP CONSTRAINT IF EXISTS parallel_groups_join_contract,
  DROP CONSTRAINT IF EXISTS parallel_groups_kind_contract,
  DROP COLUMN IF EXISTS continuation_kind,
  DROP COLUMN IF EXISTS quorum_count,
  DROP COLUMN IF EXISTS step_id,
  DROP COLUMN IF EXISTS group_kind;
