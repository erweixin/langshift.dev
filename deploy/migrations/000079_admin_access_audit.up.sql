BEGIN;

CREATE TABLE contracts.admin_access_audit (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  actor_user_id uuid NOT NULL,
  membership_id uuid NOT NULL,
  session_id uuid NOT NULL,
  action text NOT NULL,
  resource_kind text NOT NULL,
  request_id text NOT NULL,
  reason text NOT NULL,
  reason_hash text NOT NULL,
  occurred_at timestamptz NOT NULL,
  CONSTRAINT admin_access_audit_tenant_id_id_unique UNIQUE (tenant_id,id),
  CONSTRAINT admin_access_audit_request_unique UNIQUE (tenant_id,action,request_id),
  CONSTRAINT admin_access_audit_scope CHECK (
    action IN ('usage_snapshot_read','audit_export_read')
    AND resource_kind IN ('usage_snapshot','audit_export')
    AND length(btrim(request_id)) BETWEEN 1 AND 256
    AND length(btrim(reason)) BETWEEN 1 AND 500
    AND reason_hash ~ '^[0-9a-f]{64}$'
  ),
  CONSTRAINT admin_access_audit_tenant_fk FOREIGN KEY (tenant_id)
    REFERENCES identity.tenants(id) ON DELETE RESTRICT,
  CONSTRAINT admin_access_audit_actor_fk FOREIGN KEY (actor_user_id)
    REFERENCES identity.users(id) ON DELETE RESTRICT,
  CONSTRAINT admin_access_audit_membership_exact_fk FOREIGN KEY (tenant_id,membership_id)
    REFERENCES identity.memberships(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT admin_access_audit_session_fk FOREIGN KEY (session_id)
    REFERENCES identity.sessions(id) ON DELETE RESTRICT
);

ALTER TABLE contracts.admin_access_audit ENABLE ROW LEVEL SECURITY;
ALTER TABLE contracts.admin_access_audit FORCE ROW LEVEL SECURITY;
CREATE POLICY admin_access_audit_tenant_isolation ON contracts.admin_access_audit
  USING (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid);
CREATE TRIGGER admin_access_audit_append_only BEFORE UPDATE OR DELETE ON contracts.admin_access_audit
  FOR EACH ROW EXECUTE FUNCTION agent.reject_append_only_mutation();
CREATE INDEX admin_access_audit_timeline_idx ON contracts.admin_access_audit(tenant_id,occurred_at DESC,id DESC);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_contract_service') THEN
    GRANT SELECT,INSERT ON contracts.admin_access_audit TO lites_contract_service;
  END IF;
END
$$;

COMMIT;
