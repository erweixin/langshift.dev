ALTER TABLE agent.behavior_channel_deployments
  ADD CONSTRAINT behavior_channel_run_binding_unique
  UNIQUE (tenant_id,channel_id,sequence,snapshot_id,profile_name,environment);

ALTER TABLE agent.runs
  ADD COLUMN behavior_profile text,
  ADD COLUMN behavior_environment text,
  ADD COLUMN behavior_channel_id uuid,
  ADD COLUMN behavior_channel_sequence bigint,
  ADD CONSTRAINT runs_behavior_binding_contract CHECK (
    (behavior_profile IS NULL AND behavior_environment IS NULL AND behavior_channel_id IS NULL AND behavior_channel_sequence IS NULL)
    OR
    (behavior_profile IN ('route_planner','daily_planner','coach','evaluator','artifact_builder')
      AND behavior_environment IN ('staging','production')
      AND behavior_channel_id IS NOT NULL AND behavior_channel_sequence>0
      AND profile_snapshot_id ~ '^behavior-[0-9a-f]{64}$')
  ),
  ADD CONSTRAINT runs_behavior_deployment_fk FOREIGN KEY
    (tenant_id,behavior_channel_id,behavior_channel_sequence,profile_snapshot_id,behavior_profile,behavior_environment)
    REFERENCES agent.behavior_channel_deployments
    (tenant_id,channel_id,sequence,snapshot_id,profile_name,environment)
    ON DELETE RESTRICT;

CREATE INDEX runs_behavior_snapshot_idx
  ON agent.runs(tenant_id,behavior_profile,behavior_environment,profile_snapshot_id)
  WHERE behavior_channel_id IS NOT NULL;
