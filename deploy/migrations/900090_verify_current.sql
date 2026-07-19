DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='product' AND table_name='byok_credentials' AND column_name='secret_hint' AND is_nullable='NO')
    OR NOT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema='product' AND table_name='settings_access_audit')
    OR NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='settings_access_audit_append_only' AND NOT tgisinternal)
    OR NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='memory_policies_lifecycle_contract') THEN
    RAISE EXCEPTION 'settings security boundary schema is incomplete';
  END IF;
  IF has_table_privilege('public','product.byok_credentials','SELECT')
    OR has_table_privilege('public','product.memory_policies','SELECT')
    OR has_table_privilege('public','product.settings_access_audit','SELECT') THEN
    RAISE EXCEPTION 'settings security tables are exposed to PUBLIC';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service') AND (
    NOT has_table_privilege('lites_product_service','identity.sessions','SELECT')
    OR NOT has_table_privilege('lites_product_service','product.byok_credentials','SELECT,INSERT,UPDATE')
    OR NOT has_table_privilege('lites_product_service','product.byok_credential_versions','SELECT,INSERT')
    OR NOT has_table_privilege('lites_product_service','product.memory_policies','SELECT,INSERT,UPDATE')
    OR NOT has_table_privilege('lites_product_service','product.settings_access_audit','SELECT,INSERT')
  ) THEN
    RAISE EXCEPTION 'product settings least-privilege contract is incomplete';
  END IF;
END
$$;

SELECT json_build_object('status','passed','schema_version',90,'byok','vault_version_bound','memory_policy','versioned','reauthentication_minutes',5) AS current_schema_verification;
