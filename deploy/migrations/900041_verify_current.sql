BEGIN;

DO $verify$
DECLARE source text;
BEGIN
  IF (SELECT count(*) FROM public.lites_schema_migrations)<>41
    OR NOT EXISTS (
      SELECT 1 FROM public.lites_schema_migrations
      WHERE version=41 AND name='run_cancellation_recovery_discovery'
    )
  THEN
    RAISE EXCEPTION 'expected migration version 41';
  END IF;

  SELECT prosrc INTO source FROM pg_proc
  WHERE oid='agent.list_run_cancellation_tenants(uuid,uuid,integer,integer,integer,timestamp with time zone)'::regprocedure
    AND prosecdef
    AND 'row_security=off'=ANY(proconfig)
    AND pg_get_function_result(oid)='TABLE(tenant_id text)';
  IF source IS NULL
    OR position('c.store_epoch=p_store_epoch' IN source)=0
    OR position('e.store_epoch=p_store_epoch' IN source)=0
    OR position('SELECT c.tenant_id::text' IN source)=0
  THEN
    RAISE EXCEPTION 'run cancellation tenant discovery is not epoch-fenced and tenant-id-only';
  END IF;
END
$verify$;

ROLLBACK;
SELECT json_build_object(
  'status','passed',
  'schema_version',41,
  'run_cancellation_recovery_discovery','security_definer_epoch_fenced_tenant_id_only'
) AS current_schema_verification;
