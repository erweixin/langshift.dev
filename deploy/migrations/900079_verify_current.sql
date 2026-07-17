DO $$
DECLARE role_exists boolean;
BEGIN
  IF to_regclass('contracts.admin_access_audit') IS NULL THEN
    RAISE EXCEPTION 'contracts.admin_access_audit is missing';
  END IF;
  IF NOT (SELECT relrowsecurity AND relforcerowsecurity FROM pg_class WHERE oid='contracts.admin_access_audit'::regclass) THEN
    RAISE EXCEPTION 'contracts.admin_access_audit RLS is not forced';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid='contracts.admin_access_audit'::regclass AND tgname='admin_access_audit_append_only' AND NOT tgisinternal) THEN
    RAISE EXCEPTION 'admin access audit append-only trigger is missing';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='contracts.admin_access_audit'::regclass AND conname='admin_access_audit_request_unique') THEN
    RAISE EXCEPTION 'admin access audit request uniqueness is missing';
  END IF;
  SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_contract_service') INTO role_exists;
  IF role_exists AND (NOT has_table_privilege('lites_contract_service','contracts.admin_access_audit','SELECT') OR NOT has_table_privilege('lites_contract_service','contracts.admin_access_audit','INSERT')) THEN
    RAISE EXCEPTION 'contract service admin access audit privileges are incomplete';
  END IF;
END
$$;
SELECT json_build_object('status','passed','schema_version',79,'admin_access_audit','append_only_actor_session_reason_bound') AS current_schema_verification;
