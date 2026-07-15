ALTER TABLE agent.parallel_groups
  DROP CONSTRAINT parallel_groups_kind_contract,
  DROP CONSTRAINT parallel_groups_continuation_contract,
  ADD CONSTRAINT parallel_groups_kind_contract CHECK (
    group_kind IN ('execution','approval_preview','approval_direct')
  ),
  ADD CONSTRAINT parallel_groups_continuation_contract CHECK (
    continuation_kind IN ('resume','request_approval')
    AND (group_kind<>'execution' OR continuation_kind='resume')
    AND (group_kind NOT IN ('approval_preview','approval_direct') OR continuation_kind='request_approval')
  );

CREATE TABLE agent.tool_proposals (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  run_id uuid NOT NULL,
  group_id uuid NOT NULL,
  tool_call_id uuid NOT NULL,
  approval_id uuid NOT NULL,
  proposal_hash text NOT NULL,
  policy_snapshot text NOT NULL,
  scope_snapshot_ref text NOT NULL,
  scope_snapshot_hash text NOT NULL,
  execute_command_ref text NOT NULL,
  execute_command_hash text NOT NULL,
  queue_class text NOT NULL,
  resource_class text NOT NULL,
  priority integer NOT NULL,
  cost_units bigint NOT NULL,
  max_attempts integer NOT NULL,
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CONSTRAINT tool_proposals_scope_contract CHECK (
    proposal_hash ~ '^[0-9a-f]{64}$'
    AND NULLIF(policy_snapshot,'') IS NOT NULL
    AND NULLIF(scope_snapshot_ref,'') IS NOT NULL
    AND scope_snapshot_hash ~ '^[0-9a-f]{64}$'
    AND NULLIF(execute_command_ref,'') IS NOT NULL
    AND execute_command_hash ~ '^[0-9a-f]{64}$'
    AND queue_class IN ('interactive','background')
    AND NULLIF(resource_class,'') IS NOT NULL
    AND priority BETWEEN 0 AND 1000
    AND cost_units BETWEEN 1 AND 1000000000000
    AND max_attempts BETWEEN 1 AND 100
  ),
  CONSTRAINT tool_proposals_tenant_id_id_unique UNIQUE (tenant_id,id),
  CONSTRAINT tool_proposals_tool_once UNIQUE (tenant_id,tool_call_id),
  CONSTRAINT tool_proposals_approval_once UNIQUE (tenant_id,approval_id),
  CONSTRAINT tool_proposals_tenant_run_fk FOREIGN KEY (tenant_id,run_id)
    REFERENCES agent.runs(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT tool_proposals_tenant_group_fk FOREIGN KEY (tenant_id,group_id)
    REFERENCES agent.parallel_groups(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT tool_proposals_tenant_tool_fk FOREIGN KEY (tenant_id,tool_call_id)
    REFERENCES agent.tool_calls(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT tool_proposals_tenant_approval_fk FOREIGN KEY (tenant_id,approval_id)
    REFERENCES agent.approvals(tenant_id,id) ON DELETE RESTRICT
);

ALTER TABLE agent.tool_proposals ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent.tool_proposals FORCE ROW LEVEL SECURITY;
CREATE POLICY tool_proposals_tenant_isolation ON agent.tool_proposals
  USING (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid);

CREATE TRIGGER tool_proposals_append_only
  BEFORE UPDATE OR DELETE ON agent.tool_proposals
  FOR EACH ROW EXECUTE FUNCTION agent.reject_append_only_mutation();

CREATE INDEX tool_proposals_group_idx
  ON agent.tool_proposals(tenant_id,group_id,approval_id,tool_call_id);
