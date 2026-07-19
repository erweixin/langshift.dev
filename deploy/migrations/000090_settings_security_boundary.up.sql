BEGIN;

ALTER TABLE product.byok_credentials
  ADD COLUMN IF NOT EXISTS secret_hint text NOT NULL DEFAULT '••••';

ALTER TABLE product.byok_credentials
  DROP CONSTRAINT IF EXISTS byok_credentials_secret_hint_contract,
  ADD CONSTRAINT byok_credentials_secret_hint_contract CHECK (char_length(secret_hint) BETWEEN 4 AND 32 AND secret_hint !~ '[\r\n]');

ALTER TABLE product.memory_policies
  DROP CONSTRAINT IF EXISTS memory_policies_lifecycle_contract,
  ADD CONSTRAINT memory_policies_lifecycle_contract CHECK (
    version>0 AND (
      enabled AND retention_days BETWEEN 1 AND 3650 AND jsonb_typeof(allowed_kinds)='array' AND jsonb_array_length(allowed_kinds) BETWEEN 1 AND 4
      OR NOT enabled AND retention_days IS NULL AND allowed_kinds='[]'::jsonb
    )
  );

CREATE TABLE IF NOT EXISTS product.settings_access_audit (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES identity.tenants(id) ON DELETE RESTRICT,
  user_id uuid NOT NULL REFERENCES identity.users(id) ON DELETE RESTRICT,
  session_id uuid NOT NULL REFERENCES identity.sessions(id) ON DELETE RESTRICT,
  action text NOT NULL CHECK (action IN ('byok_listed','byok_configured','byok_deleted','memory_policy_read','memory_policy_updated')),
  resource_id uuid,
  request_id text NOT NULL CHECK (char_length(request_id) BETWEEN 1 AND 200),
  occurred_at timestamptz NOT NULL
);

ALTER TABLE product.settings_access_audit ENABLE ROW LEVEL SECURITY;
ALTER TABLE product.settings_access_audit FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS settings_access_audit_tenant_isolation ON product.settings_access_audit;
CREATE POLICY settings_access_audit_tenant_isolation ON product.settings_access_audit
  USING (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid);
DROP TRIGGER IF EXISTS settings_access_audit_append_only ON product.settings_access_audit;
CREATE TRIGGER settings_access_audit_append_only BEFORE UPDATE OR DELETE ON product.settings_access_audit
  FOR EACH ROW EXECUTE FUNCTION agent.reject_append_only_mutation();

REVOKE ALL ON product.byok_credentials,product.byok_credential_versions,product.memory_policies,product.settings_access_audit FROM PUBLIC;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service') THEN
    GRANT SELECT ON identity.sessions TO lites_product_service;
    GRANT SELECT,INSERT,UPDATE ON product.byok_credentials,product.memory_policies TO lites_product_service;
    GRANT SELECT,INSERT ON product.byok_credential_versions,product.settings_access_audit TO lites_product_service;
  END IF;
END
$$;

COMMIT;
