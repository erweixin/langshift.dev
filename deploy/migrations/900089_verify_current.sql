DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='identity' AND table_name='users' AND column_name='timezone' AND is_nullable='NO'
  ) OR NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='identity' AND table_name='users' AND column_name='display_name'
  ) OR NOT EXISTS (
    SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
    WHERE n.nspname='identity' AND p.proname='list_user_tenants' AND p.prosecdef
  ) THEN
    RAISE EXCEPTION 'account and tenant surface schema is incomplete';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_identity_service') AND (
    EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_identity_service' AND rolbypassrls)
    OR NOT has_function_privilege('lites_identity_service','identity.list_user_tenants(uuid,uuid,uuid,timestamptz,uuid,integer)','EXECUTE')
  ) THEN
    RAISE EXCEPTION 'identity tenant list privilege contract is incomplete';
  END IF;
  IF has_function_privilege('public','identity.list_user_tenants(uuid,uuid,uuid,timestamptz,uuid,integer)','EXECUTE') THEN
    RAISE EXCEPTION 'tenant list function is executable by PUBLIC';
  END IF;
END
$$;

SELECT json_build_object('status','passed','schema_version',89,'account_profile','versioned','tenant_listing','session_bound') AS current_schema_verification;
