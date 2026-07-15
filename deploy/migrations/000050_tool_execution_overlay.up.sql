CREATE TABLE agent.platform_tool_execution_overlays (
  id uuid PRIMARY KEY,
  tool_name text NOT NULL,
  descriptor_snapshot_id text NOT NULL,
  descriptor_hash text NOT NULL,
  policy_version bigint NOT NULL,
  decision text NOT NULL,
  reason_code text NOT NULL,
  approved_by text NOT NULL,
  approval_evidence_hash text NOT NULL,
  effective_at timestamptz NOT NULL,
  expires_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CONSTRAINT platform_tool_overlay_version_unique
    UNIQUE (tool_name,descriptor_snapshot_id,descriptor_hash,policy_version),
  CONSTRAINT platform_tool_overlay_scope_contract CHECK (
    tool_name ~ '^[a-z][a-z0-9_]{0,63}$'
    AND split_part(descriptor_snapshot_id,'@',1)=tool_name
    AND descriptor_snapshot_id ~ '^[a-z][a-z0-9_]{0,63}@[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$'
    AND descriptor_hash ~ '^[0-9a-f]{64}$'
    AND policy_version>0
    AND decision IN ('allow','deny')
    AND reason_code ~ '^[a-z][a-z0-9_.:-]{0,127}$'
    AND NULLIF(approved_by,'') IS NOT NULL
    AND approval_evidence_hash ~ '^[0-9a-f]{64}$'
    AND (expires_at IS NULL OR expires_at>effective_at)
  )
);

CREATE TABLE agent.tenant_tool_execution_overlays (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  tool_name text NOT NULL,
  descriptor_snapshot_id text NOT NULL,
  descriptor_hash text NOT NULL,
  policy_version bigint NOT NULL,
  decision text NOT NULL,
  reason_code text NOT NULL,
  approved_by uuid NOT NULL,
  approval_evidence_hash text NOT NULL,
  effective_at timestamptz NOT NULL,
  expires_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CONSTRAINT tenant_tool_overlay_version_unique
    UNIQUE (tenant_id,tool_name,descriptor_snapshot_id,descriptor_hash,policy_version),
  CONSTRAINT tenant_tool_overlay_id_scope_unique UNIQUE (tenant_id,id),
  CONSTRAINT tenant_tool_overlay_scope_contract CHECK (
    tool_name ~ '^[a-z][a-z0-9_]{0,63}$'
    AND split_part(descriptor_snapshot_id,'@',1)=tool_name
    AND descriptor_snapshot_id ~ '^[a-z][a-z0-9_]{0,63}@[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$'
    AND descriptor_hash ~ '^[0-9a-f]{64}$'
    AND policy_version>0
    AND decision IN ('allow','deny')
    AND reason_code ~ '^[a-z][a-z0-9_.:-]{0,127}$'
    AND approval_evidence_hash ~ '^[0-9a-f]{64}$'
    AND (expires_at IS NULL OR expires_at>effective_at)
  ),
  CONSTRAINT tenant_tool_overlay_tenant_fk FOREIGN KEY (tenant_id)
    REFERENCES identity.tenants(id) ON DELETE RESTRICT,
  CONSTRAINT tenant_tool_overlay_approver_fk FOREIGN KEY (approved_by)
    REFERENCES identity.users(id) ON DELETE RESTRICT
);

CREATE TRIGGER platform_tool_execution_overlays_append_only
  BEFORE UPDATE OR DELETE ON agent.platform_tool_execution_overlays
  FOR EACH ROW EXECUTE FUNCTION agent.reject_append_only_mutation();

CREATE TRIGGER tenant_tool_execution_overlays_append_only
  BEFORE UPDATE OR DELETE ON agent.tenant_tool_execution_overlays
  FOR EACH ROW EXECUTE FUNCTION agent.reject_append_only_mutation();

ALTER TABLE agent.tenant_tool_execution_overlays ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent.tenant_tool_execution_overlays FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_tool_execution_overlays_tenant_isolation
  ON agent.tenant_tool_execution_overlays
  USING (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid);

CREATE INDEX platform_tool_execution_overlays_current_idx
  ON agent.platform_tool_execution_overlays
  (tool_name,descriptor_snapshot_id,descriptor_hash,effective_at DESC,policy_version DESC);

CREATE INDEX tenant_tool_execution_overlays_current_idx
  ON agent.tenant_tool_execution_overlays
  (tenant_id,tool_name,descriptor_snapshot_id,descriptor_hash,effective_at DESC,policy_version DESC);
