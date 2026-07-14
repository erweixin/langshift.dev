DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM agent.parallel_groups) THEN
    RAISE EXCEPTION 'parallel group v7 migration requires an explicit member backfill before upgrade';
  END IF;
END
$$;

ALTER TABLE agent.parallel_groups
  ADD COLUMN group_kind text,
  ADD COLUMN step_id text,
  ADD COLUMN quorum_count integer,
  ADD COLUMN continuation_kind text;

ALTER TABLE agent.parallel_groups
  ALTER COLUMN group_kind SET NOT NULL,
  ALTER COLUMN step_id SET NOT NULL,
  ALTER COLUMN quorum_count SET NOT NULL,
  ALTER COLUMN continuation_kind SET NOT NULL,
  ADD CONSTRAINT parallel_groups_kind_contract CHECK (group_kind IN ('execution','approval_preview')),
  ADD CONSTRAINT parallel_groups_join_contract CHECK (
    join_policy IN ('all','any','quorum') AND required_count>0 AND required_count<=64
    AND quorum_count>0 AND quorum_count<=required_count
    AND (join_policy<>'all' OR quorum_count=required_count)
    AND (join_policy<>'any' OR quorum_count=1)
  ),
  ADD CONSTRAINT parallel_groups_continuation_contract CHECK (
    continuation_kind IN ('resume','request_approval')
    AND (group_kind<>'execution' OR continuation_kind='resume')
    AND (group_kind<>'approval_preview' OR continuation_kind='request_approval')
  ),
  ADD CONSTRAINT parallel_groups_step_contract CHECK (step_id<>'' AND length(step_id)<=256),
  ADD CONSTRAINT parallel_groups_tenant_id_id_unique UNIQUE (tenant_id,id),
  ADD CONSTRAINT parallel_groups_step_unique UNIQUE (tenant_id,run_id,step_id,group_kind);

CREATE TABLE agent.parallel_group_members (
  tenant_id uuid NOT NULL,
  group_id uuid NOT NULL,
  tool_call_id uuid NOT NULL,
  required boolean NOT NULL,
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (tenant_id,group_id,tool_call_id),
  CONSTRAINT parallel_group_members_group_fk FOREIGN KEY (tenant_id,group_id)
    REFERENCES agent.parallel_groups(tenant_id,id) ON DELETE CASCADE,
  CONSTRAINT parallel_group_members_tool_call_fk FOREIGN KEY (tenant_id,tool_call_id)
    REFERENCES agent.tool_calls(tenant_id,id) ON DELETE CASCADE
);

ALTER TABLE agent.parallel_group_members ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent.parallel_group_members FORCE ROW LEVEL SECURITY;
CREATE POLICY parallel_group_members_tenant_isolation ON agent.parallel_group_members
  USING (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid);

CREATE FUNCTION agent.verify_parallel_group_membership() RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  target_tenant uuid;
  target_group uuid;
  expected_required integer;
  actual_required integer;
  actual_total integer;
BEGIN
  IF TG_TABLE_NAME='parallel_groups' THEN
    target_tenant:=COALESCE(NEW.tenant_id,OLD.tenant_id);
    target_group:=COALESCE(NEW.id,OLD.id);
  ELSE
    target_tenant:=COALESCE(NEW.tenant_id,OLD.tenant_id);
    target_group:=COALESCE(NEW.group_id,OLD.group_id);
  END IF;
  SELECT required_count INTO expected_required
    FROM agent.parallel_groups WHERE tenant_id=target_tenant AND id=target_group;
  IF NOT FOUND THEN RETURN NULL; END IF;
  SELECT count(*) FILTER (WHERE required),count(*) INTO actual_required,actual_total
    FROM agent.parallel_group_members WHERE tenant_id=target_tenant AND group_id=target_group;
  IF actual_required<>expected_required OR actual_total<expected_required OR actual_total>64 THEN
    RAISE EXCEPTION 'parallel group membership is incomplete or inconsistent' USING ERRCODE='23514';
  END IF;
  RETURN NULL;
END
$$;

CREATE CONSTRAINT TRIGGER parallel_groups_membership_complete
AFTER INSERT OR UPDATE ON agent.parallel_groups
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION agent.verify_parallel_group_membership();

CREATE CONSTRAINT TRIGGER parallel_group_members_membership_complete
AFTER INSERT OR UPDATE OR DELETE ON agent.parallel_group_members
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION agent.verify_parallel_group_membership();

CREATE INDEX parallel_group_members_tool_idx
  ON agent.parallel_group_members(tenant_id,tool_call_id,group_id);
