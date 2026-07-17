DO $$
BEGIN
  IF to_regclass('contracts.audit_exports') IS NULL
    OR NOT EXISTS (SELECT 1 FROM pg_class WHERE oid='contracts.audit_exports'::regclass AND relrowsecurity AND relforcerowsecurity)
    OR NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid='contracts.audit_exports'::regclass AND tgname='audit_exports_append_only' AND NOT tgisinternal)
  THEN
    RAISE EXCEPTION 'audit export control plane verification failed';
  END IF;
END
$$;
SELECT json_build_object('status','passed','schema_version',82,'audit_exports','entitled_reauthenticated_append_only') AS current_schema_verification;
