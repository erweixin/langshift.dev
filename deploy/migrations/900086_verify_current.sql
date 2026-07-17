DO $$
BEGIN
  IF to_regclass('identity.account_erasure_receipts') IS NULL
    OR to_regclass('identity.subject_erasure_tombstones') IS NULL
    OR NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='identity' AND table_name='onboarding_sessions' AND column_name='mission_id' AND udt_name='uuid')
    OR to_regprocedure('identity.list_onboarding_route_reconciliation_tenants(uuid,integer)') IS NULL
    OR has_function_privilege('public','identity.list_onboarding_route_reconciliation_tenants(uuid,integer)','EXECUTE')
    OR NOT EXISTS (SELECT 1 FROM pg_proc WHERE oid='identity.list_onboarding_route_reconciliation_tenants(uuid,integer)'::regprocedure AND prosecdef)
  THEN RAISE EXCEPTION 'onboarding route preview pipeline contract is incomplete';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service') AND (
    EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service' AND rolbypassrls)
    OR NOT has_table_privilege('lites_product_service','product.role_profiles','SELECT,INSERT')
    OR NOT has_table_privilege('lites_product_service','identity.onboarding_sessions','SELECT,UPDATE')
    OR NOT has_table_privilege('lites_product_service','identity.onboarding_claims','SELECT,INSERT')
    OR NOT has_function_privilege('lites_product_service','identity.list_onboarding_route_reconciliation_tenants(uuid,integer)','EXECUTE')
  ) THEN RAISE EXCEPTION 'product route preview reconciliation privilege contract is incomplete';
  END IF;
END
$$;

SELECT json_build_object('status','passed','schema_version',86,'onboarding_route_preview','durable_agent_pipeline') AS current_schema_verification;
