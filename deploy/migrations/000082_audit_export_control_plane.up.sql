BEGIN;

CREATE TABLE contracts.audit_exports (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  requested_by_user_id uuid NOT NULL,
  requested_by_membership_id uuid NOT NULL,
  session_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1,
  status text NOT NULL,
  format text NOT NULL,
  period_start timestamptz NOT NULL,
  period_end timestamptz NOT NULL,
  kinds text[] NOT NULL,
  record_count integer NOT NULL,
  content_ref text NOT NULL,
  content_hash text NOT NULL,
  byte_size bigint NOT NULL,
  reason_hash text NOT NULL,
  created_at timestamptz NOT NULL,
  expires_at timestamptz NOT NULL,
  UNIQUE (tenant_id,id),
  CONSTRAINT audit_exports_tenant_fk FOREIGN KEY (tenant_id) REFERENCES identity.tenants(id) ON DELETE RESTRICT,
  CONSTRAINT audit_exports_membership_fk FOREIGN KEY (tenant_id,requested_by_membership_id) REFERENCES identity.memberships(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT audit_exports_contract CHECK (
    version=1 AND status='ready' AND format IN ('jsonl','csv')
    AND period_end>period_start AND period_end-period_start<=interval '366 days'
    AND cardinality(kinds) BETWEEN 1 AND 3
    AND kinds <@ ARRAY['contract_change','accounting_adjustment','admin_read']::text[]
    AND record_count BETWEEN 0 AND 100000
    AND length(btrim(content_ref)) BETWEEN 1 AND 2048
    AND content_hash ~ '^[0-9a-f]{64}$' AND byte_size>=0
    AND reason_hash ~ '^[0-9a-f]{64}$'
    AND expires_at>created_at
  )
);

ALTER TABLE contracts.admin_access_audit DROP CONSTRAINT admin_access_audit_scope;
ALTER TABLE contracts.admin_access_audit ADD CONSTRAINT admin_access_audit_scope CHECK (
  action IN ('usage_snapshot_read','audit_export_read','audit_export_created','audit_export_downloaded')
  AND resource_kind IN ('usage_snapshot','audit_export')
  AND length(btrim(request_id)) BETWEEN 1 AND 256
  AND length(btrim(reason)) BETWEEN 1 AND 500
  AND reason_hash ~ '^[0-9a-f]{64}$'
);

ALTER TABLE contracts.audit_exports ENABLE ROW LEVEL SECURITY;
ALTER TABLE contracts.audit_exports FORCE ROW LEVEL SECURITY;
CREATE POLICY audit_exports_tenant_isolation ON contracts.audit_exports
  USING (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid);

CREATE TRIGGER audit_exports_append_only BEFORE UPDATE OR DELETE ON contracts.audit_exports
  FOR EACH ROW EXECUTE FUNCTION agent.reject_append_only_mutation();

CREATE INDEX audit_exports_tenant_timeline_idx ON contracts.audit_exports(tenant_id,created_at DESC,id DESC);
CREATE INDEX audit_exports_expiry_idx ON contracts.audit_exports(expires_at,id);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_contract_service') THEN
    GRANT SELECT,INSERT ON contracts.audit_exports TO lites_contract_service;
  END IF;
END
$$;

COMMIT;
