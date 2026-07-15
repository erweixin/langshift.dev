DROP INDEX IF EXISTS agent.behavior_channel_current_idx;
DROP TRIGGER IF EXISTS behavior_channel_deployments_insert ON agent.behavior_channel_deployments;
DROP TRIGGER IF EXISTS behavior_channel_deployments_append_only ON agent.behavior_channel_deployments;
DROP TRIGGER IF EXISTS behavior_evaluation_reports_append_only ON agent.behavior_evaluation_reports;
DROP TRIGGER IF EXISTS behavior_snapshots_append_only ON agent.behavior_snapshots;
DROP FUNCTION IF EXISTS agent.enforce_behavior_channel_append();
DROP TABLE IF EXISTS agent.behavior_channel_deployments;
DROP TABLE IF EXISTS agent.behavior_evaluation_reports;
DROP TABLE IF EXISTS agent.behavior_snapshots;

