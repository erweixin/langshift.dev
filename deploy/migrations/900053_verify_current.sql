BEGIN;

DO $verify$
DECLARE definition text;
BEGIN
  IF (SELECT count(*) FROM public.lites_schema_migrations)<>53
    OR NOT EXISTS (SELECT 1 FROM public.lites_schema_migrations WHERE version=53 AND name='runtime_active_execution_authority')
  THEN RAISE EXCEPTION 'expected migration version 53'; END IF;

  SELECT pg_get_functiondef(
    'agent.runtime_lock_execution_right(uuid,uuid,uuid,bigint,timestamptz)'::regprocedure
  ) INTO definition;
  definition := regexp_replace(definition,'\s','','g');
  IF definition IS NULL
    OR position('s.statusIN(''requested'',''ready'',''running'',''idle'')' in definition)=0
    OR position('t.lease_expires_at>=s.execution_lease_expires_at' in definition)=0
    OR position('a.lease_expires_at>=s.execution_lease_expires_at' in definition)=0
    OR has_function_privilege('public','agent.runtime_lock_execution_right(uuid,uuid,uuid,bigint,timestamptz)','EXECUTE')
  THEN RAISE EXCEPTION 'active runtime execution authority is incomplete'; END IF;
END
$verify$;

ROLLBACK;
SELECT json_build_object('status','passed','schema_version',53,'runtime_active_execution_authority','capability_and_current_fence_locked') AS current_schema_verification;
