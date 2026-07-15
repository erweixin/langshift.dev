\set ON_ERROR_STOP on
BEGIN;
DO $verify$
DECLARE actual integer; lifecycle text;
BEGIN
  SELECT count(*) INTO actual FROM public.lites_schema_migrations;
  IF actual<>29 OR NOT EXISTS (
    SELECT 1 FROM public.lites_schema_migrations WHERE version=29 AND name='runtime_session_protocol'
  ) THEN RAISE EXCEPTION 'expected migration version 29'; END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_class c WHERE c.oid='agent.runtime_sessions'::regclass AND c.relrowsecurity AND c.relforcerowsecurity
  ) OR NOT EXISTS (
    SELECT 1 FROM pg_class c WHERE c.oid='agent.runtime_policy_snapshots'::regclass AND c.relrowsecurity AND c.relforcerowsecurity
  ) THEN RAISE EXCEPTION 'runtime RLS contract missing'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='agent.runtime_sessions'::regclass AND conname='runtime_sessions_termination_contract')
    OR NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='agent.runtime_policy_snapshots'::regclass AND conname='runtime_policy_untrusted_contract')
  THEN RAISE EXCEPTION 'runtime safety constraints missing'; END IF;
  SELECT pg_get_functiondef('agent.enforce_runtime_session_lifecycle()'::regprocedure) INTO lifecycle;
  IF position('immutable runtime session scope mutation is forbidden' IN lifecycle)=0
    OR position('runtime session version and event must advance exactly once' IN lifecycle)=0
  THEN RAISE EXCEPTION 'runtime lifecycle trigger incomplete'; END IF;
  IF to_regprocedure('agent.list_runtime_recovery_tenants(uuid,uuid,integer,integer,integer,timestamp with time zone)') IS NULL
  THEN RAISE EXCEPTION 'runtime recovery scan missing'; END IF;
END
$verify$;
ROLLBACK;
SELECT json_build_object('status','passed','schema_version',29,'runtime_session_protocol','event_bound_fenced_cleanup_receipted') AS current_schema_verification;

