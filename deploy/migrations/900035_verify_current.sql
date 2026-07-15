\set ON_ERROR_STOP on
BEGIN;
DO $verify$
BEGIN
  IF (SELECT count(*) FROM public.lites_schema_migrations)<>35 OR NOT EXISTS (
    SELECT 1 FROM public.lites_schema_migrations WHERE version=35 AND name='runtime_host_recovery_authority'
  ) THEN RAISE EXCEPTION 'expected migration version 35'; END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_proc WHERE oid='agent.runtime_lock_owned_machine(uuid,text,bytea,uuid,uuid,uuid,uuid,text,bigint)'::regprocedure
      AND prosecdef AND proconfig @> ARRAY['row_security=off']
  ) OR NOT EXISTS (
    SELECT 1 FROM pg_proc WHERE oid='agent.runtime_lock_due_session(uuid,uuid,uuid,bigint,timestamptz)'::regprocedure
      AND prosecdef AND proconfig @> ARRAY['row_security=off']
  ) OR NOT EXISTS (
    SELECT 1 FROM pg_proc WHERE oid='agent.runtime_list_host_machines(uuid,text,bytea,uuid,integer)'::regprocedure
      AND prosecdef AND proconfig @> ARRAY['row_security=off']
  ) THEN RAISE EXCEPTION 'runtime recovery authority is not security-definer fenced'; END IF;
END
$verify$;
ROLLBACK;
SELECT json_build_object('status','passed','schema_version',35,'runtime_recovery','host_authenticated_and_deadline_fenced') AS current_schema_verification;
