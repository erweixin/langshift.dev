CREATE TABLE agent.runtime_executions (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  session_id uuid NOT NULL,
  tool_call_id uuid NOT NULL,
  version bigint NOT NULL,
  request_id text NOT NULL,
  request_hash text NOT NULL,
  status text NOT NULL,
  started_session_version bigint NOT NULL,
  started_event_id uuid NOT NULL,
  started_at timestamptz NOT NULL,
  finished_session_version bigint,
  finished_event_id uuid,
  finished_at timestamptz,
  outcome_ref text,
  outcome_payload_hash text,
  outcome_hash text,
  outcome_manifest jsonb,
  failure_code text,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  CONSTRAINT runtime_executions_tenant_id_id_unique UNIQUE (tenant_id,id),
  CONSTRAINT runtime_executions_request_unique UNIQUE (tenant_id,session_id,request_id),
  CONSTRAINT runtime_executions_scope_contract CHECK (
    version>0 AND request_id ~ '^[a-zA-Z0-9][a-zA-Z0-9._:-]{7,127}$'
    AND request_hash ~ '^[0-9a-f]{64}$'
    AND status IN ('running','completed','outcome_unknown')
    AND started_session_version>0 AND updated_at>=created_at
  ),
  CONSTRAINT runtime_executions_lifecycle_contract CHECK (
    (status='running' AND version=1 AND finished_session_version IS NULL AND finished_event_id IS NULL
      AND finished_at IS NULL AND outcome_ref IS NULL AND outcome_payload_hash IS NULL
      AND outcome_hash IS NULL AND outcome_manifest IS NULL AND failure_code IS NULL)
    OR (status='completed' AND version=2 AND finished_session_version=started_session_version+1
      AND finished_event_id IS NOT NULL AND finished_at>=started_at AND NULLIF(outcome_ref,'') IS NOT NULL
      AND outcome_payload_hash ~ '^[0-9a-f]{64}$' AND outcome_hash ~ '^[0-9a-f]{64}$'
      AND jsonb_typeof(outcome_manifest)='object' AND failure_code IS NULL)
    OR (status='outcome_unknown' AND version=2 AND finished_session_version=started_session_version+1
      AND finished_event_id IS NOT NULL AND finished_at>=started_at AND NULLIF(outcome_ref,'') IS NOT NULL
      AND outcome_payload_hash ~ '^[0-9a-f]{64}$' AND outcome_hash ~ '^[0-9a-f]{64}$'
      AND jsonb_typeof(outcome_manifest)='object'
      AND failure_code ~ '^[a-z][a-z0-9_.:-]{0,127}$')
  ),
  CONSTRAINT runtime_executions_session_fk FOREIGN KEY (tenant_id,session_id)
    REFERENCES agent.runtime_sessions(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT runtime_executions_tool_fk FOREIGN KEY (tenant_id,tool_call_id)
    REFERENCES agent.tool_calls(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT runtime_executions_started_event_fk FOREIGN KEY (started_event_id)
    REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  CONSTRAINT runtime_executions_finished_event_fk FOREIGN KEY (finished_event_id)
    REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  CONSTRAINT runtime_executions_user_fk FOREIGN KEY (user_id)
    REFERENCES identity.users(id) ON DELETE RESTRICT
);

CREATE UNIQUE INDEX runtime_executions_one_running_per_session
  ON agent.runtime_executions(tenant_id,session_id) WHERE status='running';
CREATE INDEX runtime_executions_recovery_idx
  ON agent.runtime_executions(tenant_id,status,started_at,id) WHERE status='running';

CREATE FUNCTION agent.enforce_runtime_execution_lifecycle() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'runtime execution deletion is forbidden'; END IF;
  IF OLD.status<>'running' THEN RAISE EXCEPTION 'terminal runtime execution mutation is forbidden'; END IF;
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.user_id IS DISTINCT FROM OLD.user_id OR NEW.session_id IS DISTINCT FROM OLD.session_id
    OR NEW.tool_call_id IS DISTINCT FROM OLD.tool_call_id OR NEW.request_id IS DISTINCT FROM OLD.request_id
    OR NEW.request_hash IS DISTINCT FROM OLD.request_hash
    OR NEW.started_session_version IS DISTINCT FROM OLD.started_session_version
    OR NEW.started_event_id IS DISTINCT FROM OLD.started_event_id OR NEW.started_at IS DISTINCT FROM OLD.started_at
    OR NEW.created_at IS DISTINCT FROM OLD.created_at
  THEN RAISE EXCEPTION 'immutable runtime execution scope mutation is forbidden'; END IF;
  IF NEW.version<>2 OR NEW.updated_at<OLD.updated_at OR NEW.status NOT IN ('completed','outcome_unknown')
  THEN RAISE EXCEPTION 'invalid runtime execution terminal transition'; END IF;
  RETURN NEW;
END
$$;

CREATE TRIGGER runtime_execution_lifecycle
  BEFORE UPDATE OR DELETE ON agent.runtime_executions
  FOR EACH ROW EXECUTE FUNCTION agent.enforce_runtime_execution_lifecycle();

CREATE FUNCTION agent.validate_runtime_execution_events() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE finish_type text;
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM agent.events e
    WHERE e.tenant_id=NEW.tenant_id AND e.id=NEW.started_event_id
      AND e.user_id=NEW.user_id AND e.aggregate_kind='runtime_session' AND e.aggregate_id=NEW.session_id
      AND e.aggregate_version=NEW.started_session_version AND e.event_type='RuntimeSessionExecutionStarted'
  ) THEN RAISE EXCEPTION 'runtime execution lacks matching start event'; END IF;
  IF NEW.status<>'running' THEN
    finish_type := CASE NEW.status WHEN 'completed' THEN 'RuntimeSessionIdle' ELSE 'RuntimeTerminationRequested' END;
    IF NOT EXISTS (
      SELECT 1 FROM agent.events e
      WHERE e.tenant_id=NEW.tenant_id AND e.id=NEW.finished_event_id
        AND e.user_id=NEW.user_id AND e.aggregate_kind='runtime_session' AND e.aggregate_id=NEW.session_id
        AND e.aggregate_version=NEW.finished_session_version AND e.event_type=finish_type
    ) THEN RAISE EXCEPTION 'runtime execution lacks matching finish event'; END IF;
  END IF;
  RETURN NULL;
END
$$;

CREATE CONSTRAINT TRIGGER runtime_execution_event_guard
  AFTER INSERT OR UPDATE ON agent.runtime_executions
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION agent.validate_runtime_execution_events();

CREATE FUNCTION agent.validate_runtime_session_execution_settlement() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.status='running' AND NEW.status<>'running' AND EXISTS (
    SELECT 1 FROM agent.runtime_executions x
    WHERE x.tenant_id=NEW.tenant_id AND x.session_id=NEW.id AND x.status='running'
  ) THEN RAISE EXCEPTION 'runtime session transition left an unsettled execution'; END IF;
  RETURN NULL;
END
$$;

CREATE CONSTRAINT TRIGGER runtime_session_execution_settlement_guard
  AFTER UPDATE ON agent.runtime_sessions
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION agent.validate_runtime_session_execution_settlement();

ALTER TABLE agent.runtime_executions ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent.runtime_executions FORCE ROW LEVEL SECURITY;
CREATE POLICY runtime_executions_tenant_isolation ON agent.runtime_executions
  USING (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid);
