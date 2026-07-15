\set ON_ERROR_STOP on
BEGIN;
DO $verify$
DECLARE definition text;
BEGIN
  IF (SELECT count(*) FROM public.lites_schema_migrations)<>31 OR NOT EXISTS (
    SELECT 1 FROM public.lites_schema_migrations
    WHERE version=31 AND name='runtime_execution_right_lock'
  ) THEN RAISE EXCEPTION 'expected migration version 31'; END IF;

  IF NOT EXISTS (
    SELECT 1 FROM pg_proc p
    JOIN pg_namespace n ON n.oid=p.pronamespace
    WHERE n.nspname='agent' AND p.proname='runtime_lock_execution_right'
      AND p.prosecdef AND p.prorettype='text'::regtype
      AND pg_get_function_identity_arguments(p.oid)='p_tenant_id uuid, p_session_id uuid, p_approval_id uuid, p_approval_version bigint, p_now timestamp with time zone'
  ) THEN RAISE EXCEPTION 'runtime execution-right lock function missing or not security definer'; END IF;

  SELECT pg_get_functiondef('agent.runtime_lock_execution_right(uuid,uuid,uuid,bigint,timestamptz)'::regprocedure) INTO definition;
  IF position('FOR SHARE OF t,a' IN definition)=0
    OR position('t.status=''executing''' IN definition)=0
    OR position('a.status=''running''' IN definition)=0
    OR position('t.current_fence=s.execution_fence' IN definition)=0
    OR position('a.status=''granted''' IN definition)=0
    OR position('ApprovalGranted' IN definition)=0
  THEN RAISE EXCEPTION 'runtime execution-right lock is incomplete'; END IF;

  IF EXISTS (
    SELECT 1 FROM information_schema.routine_privileges
    WHERE routine_schema='agent' AND routine_name='runtime_lock_execution_right'
      AND grantee='PUBLIC' AND privilege_type='EXECUTE'
  ) THEN
    RAISE EXCEPTION 'runtime execution-right lock is public';
  END IF;
END
$verify$;
ROLLBACK;
SELECT json_build_object('status','passed','schema_version',31,'runtime_execution_right','transactionally_locked') AS current_schema_verification;
