BEGIN;

DO $verify$
BEGIN
  IF (SELECT count(*) FROM public.lites_schema_migrations)<>51
    OR NOT EXISTS (SELECT 1 FROM public.lites_schema_migrations WHERE version=51 AND name='runtime_session_request_binding')
  THEN RAISE EXCEPTION 'expected migration version 51'; END IF;
  IF NOT EXISTS (
      SELECT 1 FROM information_schema.columns
      WHERE table_schema='agent' AND table_name='runtime_sessions' AND column_name='approval_id'
    ) OR NOT EXISTS (
      SELECT 1 FROM information_schema.columns
      WHERE table_schema='agent' AND table_name='runtime_sessions' AND column_name='approval_version'
    ) OR NOT EXISTS (
      SELECT 1 FROM pg_constraint
      WHERE conrelid='agent.runtime_sessions'::regclass AND conname='runtime_sessions_approval_binding_contract'
    ) OR NOT EXISTS (
      SELECT 1 FROM pg_trigger
      WHERE tgrelid='agent.runtime_sessions'::regclass AND tgname='runtime_session_request_binding_immutable' AND tgenabled='O'
    ) OR position('lease_expires_at>=s.execution_lease_expires_at' IN regexp_replace(pg_get_functiondef(
      'agent.runtime_lock_execution_right(uuid,uuid,uuid,bigint,timestamptz)'::regprocedure
    ),'\s','','g'))=0
  THEN RAISE EXCEPTION 'runtime session request binding contract is incomplete'; END IF;
END
$verify$;

ROLLBACK;
SELECT json_build_object('status','passed','schema_version',51,'runtime_session_request_binding','immutable_approval_authority') AS current_schema_verification;
