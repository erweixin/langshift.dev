DROP INDEX IF EXISTS agent.runs_behavior_snapshot_idx;
ALTER TABLE agent.runs
  DROP CONSTRAINT IF EXISTS runs_behavior_deployment_fk,
  DROP CONSTRAINT IF EXISTS runs_behavior_binding_contract,
  DROP COLUMN IF EXISTS behavior_channel_sequence,
  DROP COLUMN IF EXISTS behavior_channel_id,
  DROP COLUMN IF EXISTS behavior_environment,
  DROP COLUMN IF EXISTS behavior_profile;
ALTER TABLE agent.behavior_channel_deployments
  DROP CONSTRAINT IF EXISTS behavior_channel_run_binding_unique;
