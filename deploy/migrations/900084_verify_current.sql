DO $$
BEGIN
  IF to_regclass('identity.account_erasure_receipts') IS NULL
    OR to_regclass('identity.subject_erasure_tombstones') IS NULL
    OR NOT EXISTS (SELECT 1 FROM pg_class WHERE oid='identity.account_erasure_receipts'::regclass AND relrowsecurity AND relforcerowsecurity)
    OR NOT EXISTS (SELECT 1 FROM pg_class WHERE oid='identity.subject_erasure_tombstones'::regclass AND relrowsecurity AND relforcerowsecurity)
    OR NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid='identity.account_erasure_receipts'::regclass AND tgname='account_erasure_receipts_append_only' AND NOT tgisinternal)
    OR NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid='identity.subject_erasure_tombstones'::regclass AND tgname='subject_erasure_tombstones_append_only' AND NOT tgisinternal)
    OR to_regprocedure('identity.list_subject_erasure_tombstones_missing_epoch(uuid,integer)') IS NULL
    OR to_regprocedure('identity.list_subject_erasure_tenants(uuid)') IS NULL
    OR has_function_privilege('public','identity.list_subject_erasure_tombstones_missing_epoch(uuid,integer)','EXECUTE')
    OR has_function_privilege('public','identity.list_subject_erasure_tenants(uuid)','EXECUTE')
    OR NOT EXISTS (
      SELECT 1 FROM pg_proc
      WHERE oid='identity.list_subject_erasure_tombstones_missing_epoch(uuid,integer)'::regprocedure
        AND prosecdef
        AND proconfig @> ARRAY['search_path=pg_catalog, identity']::text[]
    ) THEN
    RAISE EXCEPTION 'account erasure receipt protocol is incomplete';
  END IF;
END
$$;
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_erasure_worker') AND (
    EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_erasure_worker' AND rolbypassrls)
    OR NOT has_function_privilege('lites_erasure_worker','identity.list_subject_erasure_tombstones_missing_epoch(uuid,integer)','EXECUTE')
    OR NOT has_function_privilege('lites_erasure_worker','identity.list_subject_erasure_tenants(uuid)','EXECUTE')
    OR NOT has_table_privilege('lites_erasure_worker','identity.account_erasure_requests','SELECT,UPDATE')
    OR NOT has_table_privilege('lites_erasure_worker','identity.account_erasure_receipts','SELECT,INSERT')
    OR NOT has_table_privilege('lites_erasure_worker','identity.subject_erasure_tombstones','SELECT,INSERT')
    OR NOT has_table_privilege('lites_erasure_worker','agent.memory_revision_subjects','SELECT')
    OR NOT has_table_privilege('lites_erasure_worker','agent.memory_revision_derivations','SELECT')
    OR NOT has_table_privilege('lites_erasure_worker','agent.events','SELECT,INSERT')
    OR NOT has_table_privilege('lites_erasure_worker','agent.outbox','SELECT,INSERT,UPDATE')
    OR NOT has_table_privilege('lites_erasure_worker','agent.inbox','SELECT,INSERT,UPDATE')
  ) THEN
    RAISE EXCEPTION 'lites_erasure_worker privilege contract is incomplete';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_agent_service')
    AND NOT has_table_privilege('lites_agent_service','identity.subject_erasure_tombstones','SELECT') THEN
    RAISE EXCEPTION 'lites_agent_service erasure admission privilege is incomplete';
  END IF;
END
$$;
SELECT json_build_object('status','passed','schema_version',84,'account_erasure_receipts','tenant_scoped_append_only','restore_tombstones','tenant_scoped_append_only') AS current_schema_verification;
