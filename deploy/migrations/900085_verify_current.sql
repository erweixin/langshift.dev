DO $$
BEGIN
  IF to_regclass('identity.account_erasure_receipts') IS NULL
    OR to_regclass('identity.subject_erasure_tombstones') IS NULL
    OR to_regprocedure('identity.list_subject_erasure_tombstones_missing_epoch(uuid,integer)') IS NULL
    OR to_regprocedure('identity.list_subject_erasure_tenants(uuid)') IS NULL
    OR to_regclass('product.role_profiles') IS NULL
    OR NOT EXISTS (
      SELECT 1 FROM pg_class
      WHERE oid='product.role_profiles'::regclass AND relrowsecurity AND relforcerowsecurity
    ) THEN
    RAISE EXCEPTION 'current database storage contract is incomplete';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service') AND (
    EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service' AND rolbypassrls)
    OR NOT has_table_privilege('lites_product_service','product.role_profiles','SELECT,INSERT')
    OR has_table_privilege('lites_product_service','product.role_profiles','UPDATE,DELETE')
  ) THEN
    RAISE EXCEPTION 'product content catalog activation privilege contract is incomplete';
  END IF;
END
$$;

SELECT json_build_object('status','passed','schema_version',85,'product_content_catalog','tenant_scoped_insert_only') AS current_schema_verification;
