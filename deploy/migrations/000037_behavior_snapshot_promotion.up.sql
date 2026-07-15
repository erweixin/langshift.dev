CREATE TABLE agent.behavior_snapshots (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  snapshot_id text NOT NULL,
  profile_name text NOT NULL,
  manifest jsonb NOT NULL,
  manifest_hash text NOT NULL,
  source_commit text NOT NULL,
  created_by uuid NOT NULL,
  created_event_id uuid NOT NULL,
  created_at timestamptz NOT NULL,
  CONSTRAINT behavior_snapshots_tenant_snapshot_unique UNIQUE (tenant_id,snapshot_id),
  CONSTRAINT behavior_snapshots_exact_unique UNIQUE (tenant_id,snapshot_id,profile_name,manifest_hash),
  CONSTRAINT behavior_snapshots_scope_contract CHECK (
    profile_name IN ('route_planner','daily_planner','coach','evaluator','artifact_builder')
    AND manifest_hash ~ '^[0-9a-f]{64}$'
    AND snapshot_id='behavior-'||manifest_hash
    AND NULLIF(source_commit,'') IS NOT NULL
    AND jsonb_typeof(manifest)='object'
    AND (manifest->>'schema_version')::integer=1
    AND manifest->>'profile'=profile_name
    AND manifest->>'source_commit'=source_commit
  ),
  CONSTRAINT behavior_snapshots_tenant_fk FOREIGN KEY (tenant_id) REFERENCES identity.tenants(id) ON DELETE RESTRICT,
  CONSTRAINT behavior_snapshots_creator_fk FOREIGN KEY (created_by) REFERENCES identity.users(id) ON DELETE RESTRICT,
  CONSTRAINT behavior_snapshots_event_fk FOREIGN KEY (created_event_id) REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED
);

CREATE TABLE agent.behavior_evaluation_reports (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  report_id text NOT NULL,
  profile_name text NOT NULL,
  candidate_snapshot_id text NOT NULL,
  baseline_snapshot_id text NOT NULL,
  report jsonb NOT NULL,
  report_hash text NOT NULL,
  passed boolean NOT NULL,
  recorded_by uuid NOT NULL,
  recorded_event_id uuid NOT NULL,
  evaluated_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL,
  CONSTRAINT behavior_evaluation_report_unique UNIQUE (tenant_id,report_id),
  CONSTRAINT behavior_evaluation_exact_unique UNIQUE (tenant_id,id,candidate_snapshot_id,baseline_snapshot_id,report_hash),
  CONSTRAINT behavior_evaluation_scope_contract CHECK (
    profile_name IN ('route_planner','daily_planner','coach','evaluator','artifact_builder')
    AND candidate_snapshot_id<>baseline_snapshot_id
    AND report_hash ~ '^[0-9a-f]{64}$'
    AND jsonb_typeof(report)='object'
    AND (report->>'schema_version')::integer=1
    AND report->>'report_id'=report_id
    AND report->>'profile'=profile_name
    AND report->>'candidate_snapshot_id'=candidate_snapshot_id
    AND report->>'baseline_snapshot_id'=baseline_snapshot_id
  ),
  CONSTRAINT behavior_evaluation_candidate_fk FOREIGN KEY (tenant_id,candidate_snapshot_id) REFERENCES agent.behavior_snapshots(tenant_id,snapshot_id) ON DELETE RESTRICT,
  CONSTRAINT behavior_evaluation_baseline_fk FOREIGN KEY (tenant_id,baseline_snapshot_id) REFERENCES agent.behavior_snapshots(tenant_id,snapshot_id) ON DELETE RESTRICT,
  CONSTRAINT behavior_evaluation_recorder_fk FOREIGN KEY (recorded_by) REFERENCES identity.users(id) ON DELETE RESTRICT,
  CONSTRAINT behavior_evaluation_event_fk FOREIGN KEY (recorded_event_id) REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED
);

CREATE TABLE agent.behavior_channel_deployments (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  channel_id uuid NOT NULL,
  profile_name text NOT NULL,
  environment text NOT NULL,
  sequence bigint NOT NULL,
  action text NOT NULL,
  snapshot_id text NOT NULL,
  previous_snapshot_id text NOT NULL,
  evaluation_report_id uuid,
  evaluation_report_hash text,
  promotion_hash text,
  rollout_policy jsonb,
  auto_rollback_policy jsonb,
  approvals jsonb NOT NULL,
  incident_evidence_hash text,
  rollback_hash text,
  rollback_trigger text,
  rollback_observed_value double precision,
  rollback_threshold double precision,
  automation_key_id text,
  automation_signature text,
  rollback_occurred_at timestamptz,
  activated_by uuid,
  activation_event_id uuid NOT NULL,
  activated_at timestamptz NOT NULL,
  CONSTRAINT behavior_channel_sequence_unique UNIQUE (tenant_id,profile_name,environment,sequence),
  CONSTRAINT behavior_channel_id_scope_unique UNIQUE (tenant_id,id),
  CONSTRAINT behavior_channel_version_unique UNIQUE (tenant_id,channel_id,sequence),
  CONSTRAINT behavior_channel_scope_contract CHECK (
    profile_name IN ('route_planner','daily_planner','coach','evaluator','artifact_builder')
    AND environment IN ('staging','production')
    AND sequence>0 AND action IN ('promote','rollback') AND snapshot_id<>previous_snapshot_id
    AND jsonb_typeof(approvals)='array'
    AND ((action='promote' AND evaluation_report_id IS NOT NULL AND evaluation_report_hash ~ '^[0-9a-f]{64}$' AND promotion_hash ~ '^[0-9a-f]{64}$'
      AND jsonb_typeof(rollout_policy)='object' AND jsonb_typeof(auto_rollback_policy)='object'
      AND jsonb_array_length(approvals)=2 AND incident_evidence_hash IS NULL AND rollback_hash IS NULL
      AND rollback_trigger IS NULL AND rollback_observed_value IS NULL AND rollback_threshold IS NULL
      AND automation_key_id IS NULL AND automation_signature IS NULL AND rollback_occurred_at IS NULL
      AND activated_by IS NOT NULL)
    OR (action='rollback' AND evaluation_report_id IS NULL AND evaluation_report_hash IS NULL AND promotion_hash IS NULL
      AND rollout_policy IS NULL AND auto_rollback_policy IS NULL AND jsonb_array_length(approvals)=0
      AND incident_evidence_hash ~ '^[0-9a-f]{64}$' AND rollback_hash ~ '^[0-9a-f]{64}$'
      AND rollback_trigger IN ('zero_tolerance','error_rate','latency_ratio','cost_ratio','quality_ratio')
      AND rollback_observed_value IS NOT NULL AND rollback_observed_value NOT IN ('NaN'::float8,'Infinity'::float8,'-Infinity'::float8)
      AND rollback_threshold IS NOT NULL AND rollback_threshold NOT IN ('NaN'::float8,'Infinity'::float8,'-Infinity'::float8)
      AND NULLIF(automation_key_id,'') IS NOT NULL AND NULLIF(automation_signature,'') IS NOT NULL
      AND rollback_occurred_at IS NOT NULL AND activated_by IS NULL))
  ),
  CONSTRAINT behavior_channel_snapshot_fk FOREIGN KEY (tenant_id,snapshot_id) REFERENCES agent.behavior_snapshots(tenant_id,snapshot_id) ON DELETE RESTRICT,
  CONSTRAINT behavior_channel_previous_fk FOREIGN KEY (tenant_id,previous_snapshot_id) REFERENCES agent.behavior_snapshots(tenant_id,snapshot_id) ON DELETE RESTRICT,
  CONSTRAINT behavior_channel_evaluation_fk FOREIGN KEY (tenant_id,evaluation_report_id,snapshot_id,previous_snapshot_id,evaluation_report_hash)
    REFERENCES agent.behavior_evaluation_reports(tenant_id,id,candidate_snapshot_id,baseline_snapshot_id,report_hash) ON DELETE RESTRICT,
  CONSTRAINT behavior_channel_activator_fk FOREIGN KEY (activated_by) REFERENCES identity.users(id) ON DELETE RESTRICT,
  CONSTRAINT behavior_channel_event_fk FOREIGN KEY (activation_event_id) REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED
);

CREATE OR REPLACE FUNCTION agent.enforce_behavior_channel_append() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,agent SET row_security=off AS $$
DECLARE current_deployment agent.behavior_channel_deployments%ROWTYPE;
DECLARE snapshot_profile text;
DECLARE previous_profile text;
DECLARE approval_roles integer;
DECLARE approval_people integer;
BEGIN
  IF NEW.tenant_id IS DISTINCT FROM NULLIF(current_setting('lites.tenant_id',true),'')::uuid THEN
    RAISE EXCEPTION 'behavior channel tenant context mismatch' USING ERRCODE='42501';
  END IF;
  PERFORM pg_advisory_xact_lock(hashtextextended(NEW.tenant_id::text||':'||NEW.profile_name||':'||NEW.environment,0));
  IF EXISTS (SELECT 1 FROM agent.behavior_channel_deployments WHERE tenant_id=NEW.tenant_id AND id=NEW.id) THEN
    RETURN NEW;
  END IF;
  SELECT * INTO current_deployment FROM agent.behavior_channel_deployments
    WHERE tenant_id=NEW.tenant_id AND profile_name=NEW.profile_name AND environment=NEW.environment
    ORDER BY sequence DESC LIMIT 1;
  IF current_deployment.id IS NULL THEN
    IF NEW.sequence<>1 OR NEW.action<>'promote' THEN RAISE EXCEPTION 'behavior channel must begin with promotion' USING ERRCODE='40001'; END IF;
  ELSIF NEW.channel_id<>current_deployment.channel_id OR NEW.sequence<>current_deployment.sequence+1 OR NEW.previous_snapshot_id<>current_deployment.snapshot_id THEN
    RAISE EXCEPTION 'stale behavior channel deployment' USING ERRCODE='40001';
  END IF;
  SELECT profile_name INTO snapshot_profile FROM agent.behavior_snapshots WHERE tenant_id=NEW.tenant_id AND snapshot_id=NEW.snapshot_id;
  SELECT profile_name INTO previous_profile FROM agent.behavior_snapshots WHERE tenant_id=NEW.tenant_id AND snapshot_id=NEW.previous_snapshot_id;
  IF snapshot_profile IS DISTINCT FROM NEW.profile_name OR previous_profile IS DISTINCT FROM NEW.profile_name THEN
    RAISE EXCEPTION 'behavior snapshot profile mismatch' USING ERRCODE='23514';
  END IF;
  IF NEW.action='promote' THEN
	IF NOT EXISTS (SELECT 1 FROM agent.behavior_evaluation_reports evaluation
	  WHERE evaluation.tenant_id=NEW.tenant_id AND evaluation.id=NEW.evaluation_report_id AND evaluation.passed) THEN
	  RAISE EXCEPTION 'behavior evaluation did not pass' USING ERRCODE='23514';
	END IF;
    SELECT count(DISTINCT item->>'role'),count(DISTINCT item->>'approver_id') INTO approval_roles,approval_people
      FROM jsonb_array_elements(NEW.approvals) item
      WHERE item->>'role' IN ('risk_owner','release_owner') AND NULLIF(item->>'signature','') IS NOT NULL;
    IF approval_roles<>2 OR approval_people<>2 THEN RAISE EXCEPTION 'independent behavior approvals required' USING ERRCODE='23514'; END IF;
  ELSIF NOT EXISTS (
    SELECT 1 FROM agent.behavior_channel_deployments history
    WHERE history.tenant_id=NEW.tenant_id AND history.profile_name=NEW.profile_name
      AND history.environment=NEW.environment AND history.snapshot_id=NEW.snapshot_id
  ) THEN
    RAISE EXCEPTION 'rollback target was never deployed on channel' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION agent.enforce_behavior_channel_append() FROM PUBLIC;

CREATE TRIGGER behavior_snapshots_append_only BEFORE UPDATE OR DELETE ON agent.behavior_snapshots
  FOR EACH ROW EXECUTE FUNCTION agent.reject_append_only_mutation();
CREATE TRIGGER behavior_evaluation_reports_append_only BEFORE UPDATE OR DELETE ON agent.behavior_evaluation_reports
  FOR EACH ROW EXECUTE FUNCTION agent.reject_append_only_mutation();
CREATE TRIGGER behavior_channel_deployments_append_only BEFORE UPDATE OR DELETE ON agent.behavior_channel_deployments
  FOR EACH ROW EXECUTE FUNCTION agent.reject_append_only_mutation();
CREATE TRIGGER behavior_channel_deployments_insert BEFORE INSERT ON agent.behavior_channel_deployments
  FOR EACH ROW EXECUTE FUNCTION agent.enforce_behavior_channel_append();

ALTER TABLE agent.behavior_snapshots ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent.behavior_snapshots FORCE ROW LEVEL SECURITY;
CREATE POLICY behavior_snapshots_tenant_isolation ON agent.behavior_snapshots
  USING (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid);

ALTER TABLE agent.behavior_evaluation_reports ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent.behavior_evaluation_reports FORCE ROW LEVEL SECURITY;
CREATE POLICY behavior_evaluation_reports_tenant_isolation ON agent.behavior_evaluation_reports
  USING (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid);

ALTER TABLE agent.behavior_channel_deployments ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent.behavior_channel_deployments FORCE ROW LEVEL SECURITY;
CREATE POLICY behavior_channel_deployments_tenant_isolation ON agent.behavior_channel_deployments
  USING (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid);

CREATE INDEX behavior_channel_current_idx ON agent.behavior_channel_deployments(tenant_id,profile_name,environment,sequence DESC);
