DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM agent.child_groups) THEN
    RAISE EXCEPTION 'child group rows predate the complete orchestration protocol';
  END IF;
END
$$;

ALTER TABLE agent.runs
  ADD COLUMN parent_run_id uuid,
  ADD COLUMN root_run_id uuid,
  ADD COLUMN spawn_tool_call_id uuid,
  ADD COLUMN child_group_id uuid,
  ADD COLUMN depth integer NOT NULL DEFAULT 0,
  ADD COLUMN inherited_budget_microunits bigint NOT NULL DEFAULT 0,
  ADD COLUMN result_summary_ref text,
  ADD COLUMN result_summary_hash text;

UPDATE agent.runs SET root_run_id=id;

CREATE FUNCTION agent.initialize_root_run_orchestration_identity() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.parent_run_id IS NULL AND NEW.root_run_id IS NULL THEN NEW.root_run_id:=NEW.id; END IF;
  RETURN NEW;
END
$$;

CREATE TRIGGER runs_initialize_root_orchestration_identity
  BEFORE INSERT ON agent.runs FOR EACH ROW
  EXECUTE FUNCTION agent.initialize_root_run_orchestration_identity();

ALTER TABLE agent.runs
  ALTER COLUMN root_run_id SET NOT NULL,
  ADD CONSTRAINT runs_orchestration_identity_contract CHECK (
    (parent_run_id IS NULL AND root_run_id=id AND spawn_tool_call_id IS NULL
      AND child_group_id IS NULL AND depth=0 AND inherited_budget_microunits=0)
    OR
    (parent_run_id IS NOT NULL AND root_run_id<>id AND spawn_tool_call_id IS NOT NULL
      AND child_group_id IS NOT NULL AND depth BETWEEN 1 AND 5 AND inherited_budget_microunits>0)
  ),
  ADD CONSTRAINT runs_result_summary_contract CHECK (
    (result_summary_ref IS NULL AND result_summary_hash IS NULL)
    OR (status IN ('succeeded','failed','cancelled','expired')
      AND NULLIF(result_summary_ref,'') IS NOT NULL
      AND result_summary_hash ~ '^[0-9a-f]{64}$')
  ),
  ADD CONSTRAINT runs_parent_fk FOREIGN KEY (tenant_id,parent_run_id)
    REFERENCES agent.runs(tenant_id,id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  ADD CONSTRAINT runs_root_fk FOREIGN KEY (tenant_id,root_run_id)
    REFERENCES agent.runs(tenant_id,id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  ADD CONSTRAINT runs_spawn_tool_fk FOREIGN KEY (tenant_id,spawn_tool_call_id)
    REFERENCES agent.tool_calls(tenant_id,id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE agent.tool_calls
  DROP CONSTRAINT tool_calls_execution_mode_contract,
  ADD CONSTRAINT tool_calls_execution_mode_contract CHECK (
    execution_mode IN ('worker_runtime','inline_platform')
    AND (execution_mode<>'inline_platform' OR
      (tool_name='memory_write' AND effect_class='idempotent_write') OR
      (tool_name='spawn_agent_run' AND effect_class='read_only'))
  );

ALTER TABLE agent.child_groups
  DROP CONSTRAINT child_groups_parent_run_id_fk,
  ADD COLUMN step_id text,
  ADD COLUMN quorum_count integer,
  ADD COLUMN continuation_kind text,
  ADD COLUMN joined_event_id uuid,
  ADD CONSTRAINT child_groups_tenant_id_id_unique UNIQUE (tenant_id,id),
  ADD CONSTRAINT child_groups_parent_fk FOREIGN KEY (tenant_id,parent_run_id)
    REFERENCES agent.runs(tenant_id,id) ON DELETE RESTRICT,
  ADD CONSTRAINT child_groups_join_contract CHECK (
    join_policy IN ('all','any','quorum') AND required_count>0 AND required_count<=10
    AND quorum_count>0 AND quorum_count<=required_count
    AND (join_policy<>'all' OR quorum_count=required_count)
    AND (join_policy<>'any' OR quorum_count=1)
  ),
  ADD CONSTRAINT child_groups_step_contract CHECK (NULLIF(step_id,'') IS NOT NULL AND length(step_id)<=256),
  ADD CONSTRAINT child_groups_continuation_contract CHECK (continuation_kind='resume_parent'),
  ADD CONSTRAINT child_groups_joined_contract CHECK (
    (joined=false AND continuation_id IS NULL AND joined_event_id IS NULL)
    OR (joined=true AND continuation_id IS NOT NULL AND joined_event_id IS NOT NULL)
  ),
  ADD CONSTRAINT child_groups_step_unique UNIQUE (tenant_id,parent_run_id,step_id);

ALTER TABLE agent.child_groups
  ADD CONSTRAINT child_groups_continuation_fk FOREIGN KEY (tenant_id,continuation_id)
    REFERENCES agent.continuations(tenant_id,id) DEFERRABLE INITIALLY DEFERRED,
  ADD CONSTRAINT child_groups_joined_event_fk FOREIGN KEY (joined_event_id)
    REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE agent.runs
  ADD CONSTRAINT runs_child_group_fk FOREIGN KEY (tenant_id,child_group_id)
    REFERENCES agent.child_groups(tenant_id,id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;

CREATE TABLE agent.child_group_members (
  tenant_id uuid NOT NULL,
  group_id uuid NOT NULL,
  child_run_id uuid NOT NULL,
  required boolean NOT NULL,
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (tenant_id,group_id,child_run_id),
  CONSTRAINT child_group_members_group_fk FOREIGN KEY (tenant_id,group_id)
    REFERENCES agent.child_groups(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT child_group_members_run_fk FOREIGN KEY (tenant_id,child_run_id)
    REFERENCES agent.runs(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT child_group_members_child_unique UNIQUE (tenant_id,child_run_id)
);

ALTER TABLE agent.child_group_members ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent.child_group_members FORCE ROW LEVEL SECURITY;
CREATE POLICY child_group_members_tenant_isolation ON agent.child_group_members
  USING (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid);

CREATE TABLE agent.orchestration_quotas (
  tenant_id uuid NOT NULL,
  root_run_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version>0),
  max_depth integer NOT NULL DEFAULT 5,
  max_concurrent_children integer NOT NULL DEFAULT 10,
  max_total_descendants integer NOT NULL DEFAULT 1000,
  total_descendants integer NOT NULL DEFAULT 0,
  concurrent_children integer NOT NULL DEFAULT 0,
  allocated_budget_microunits bigint NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (tenant_id,root_run_id),
  CONSTRAINT orchestration_quotas_root_fk FOREIGN KEY (tenant_id,root_run_id)
    REFERENCES agent.runs(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT orchestration_quotas_limits_contract CHECK (
    max_depth BETWEEN 1 AND 5
    AND max_concurrent_children BETWEEN 1 AND 10
    AND max_total_descendants BETWEEN 1 AND 1000
    AND total_descendants BETWEEN 0 AND max_total_descendants
    AND concurrent_children BETWEEN 0 AND max_concurrent_children
    AND allocated_budget_microunits>=0
  )
);

ALTER TABLE agent.orchestration_quotas ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent.orchestration_quotas FORCE ROW LEVEL SECURITY;
CREATE POLICY orchestration_quotas_tenant_isolation ON agent.orchestration_quotas
  USING (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid);

CREATE FUNCTION agent.enforce_run_orchestration_identity() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.parent_run_id IS DISTINCT FROM OLD.parent_run_id
    OR NEW.root_run_id IS DISTINCT FROM OLD.root_run_id
    OR NEW.spawn_tool_call_id IS DISTINCT FROM OLD.spawn_tool_call_id
    OR NEW.child_group_id IS DISTINCT FROM OLD.child_group_id
    OR NEW.depth IS DISTINCT FROM OLD.depth
    OR NEW.inherited_budget_microunits IS DISTINCT FROM OLD.inherited_budget_microunits THEN
    RAISE EXCEPTION 'run orchestration identity is immutable';
  END IF;
  RETURN NEW;
END
$$;

CREATE TRIGGER runs_orchestration_identity_immutable
  BEFORE UPDATE ON agent.runs FOR EACH ROW
  EXECUTE FUNCTION agent.enforce_run_orchestration_identity();

CREATE FUNCTION agent.verify_child_group_membership() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE target_tenant uuid; target_group uuid; expected_required integer; actual_required integer; actual_total integer;
BEGIN
  IF TG_TABLE_NAME='child_groups' THEN
    target_tenant:=COALESCE(NEW.tenant_id,OLD.tenant_id);
    target_group:=COALESCE(NEW.id,OLD.id);
  ELSE
    target_tenant:=COALESCE(NEW.tenant_id,OLD.tenant_id);
    target_group:=COALESCE(NEW.group_id,OLD.group_id);
  END IF;
  SELECT required_count INTO expected_required FROM agent.child_groups
    WHERE tenant_id=target_tenant AND id=target_group;
  IF NOT FOUND THEN RETURN NULL; END IF;
  SELECT count(*) FILTER (WHERE required),count(*) INTO actual_required,actual_total
    FROM agent.child_group_members WHERE tenant_id=target_tenant AND group_id=target_group;
  IF actual_required<>expected_required OR actual_total<expected_required OR actual_total>10 THEN
    RAISE EXCEPTION 'child group membership is incomplete or inconsistent' USING ERRCODE='23514';
  END IF;
  IF EXISTS (
    SELECT 1 FROM agent.child_group_members m
    JOIN agent.child_groups g ON g.tenant_id=m.tenant_id AND g.id=m.group_id
    JOIN agent.runs r ON r.tenant_id=m.tenant_id AND r.id=m.child_run_id
    WHERE m.tenant_id=target_tenant AND m.group_id=target_group
      AND (r.parent_run_id<>g.parent_run_id OR r.child_group_id<>g.id OR r.depth<1)
  ) THEN
    RAISE EXCEPTION 'child group member is not a direct child of its parent' USING ERRCODE='23514';
  END IF;
  RETURN NULL;
END
$$;

CREATE CONSTRAINT TRIGGER child_groups_membership_complete
  AFTER INSERT OR UPDATE ON agent.child_groups
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION agent.verify_child_group_membership();
CREATE CONSTRAINT TRIGGER child_group_members_membership_complete
  AFTER INSERT OR UPDATE OR DELETE ON agent.child_group_members
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION agent.verify_child_group_membership();

CREATE INDEX runs_parent_active_idx ON agent.runs(tenant_id,parent_run_id,id)
  WHERE parent_run_id IS NOT NULL AND status IN ('accepted','queued','executing','waiting_tool','waiting_child','waiting_approval');
CREATE INDEX runs_root_tree_idx ON agent.runs(tenant_id,root_run_id,depth,id);
CREATE INDEX child_group_members_run_idx ON agent.child_group_members(tenant_id,child_run_id,group_id);
