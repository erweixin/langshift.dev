\set ON_ERROR_STOP on
BEGIN;
DO $verify$
DECLARE actual integer; allocation_lifecycle text;
BEGIN
  SELECT count(*) INTO actual FROM public.lites_schema_migrations;
  IF actual<>30 OR NOT EXISTS (
    SELECT 1 FROM public.lites_schema_migrations WHERE version=30 AND name='runtime_host_capacity_protocol'
  ) THEN RAISE EXCEPTION 'expected migration version 30'; END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_class c WHERE c.oid='agent.runtime_allocations'::regclass AND c.relrowsecurity AND c.relforcerowsecurity
  ) THEN RAISE EXCEPTION 'runtime allocation RLS contract missing'; END IF;
  IF EXISTS (
    SELECT 1 FROM pg_class c WHERE c.oid='agent.runtime_hosts'::regclass AND c.relrowsecurity
  ) THEN RAISE EXCEPTION 'global runtime hosts must not use tenant RLS'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='agent.runtime_allocations'::regclass AND conname='runtime_allocations_session_fk' AND condeferrable AND condeferred)
    OR NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid='agent.runtime_sessions'::regclass AND tgname='runtime_session_allocation_guard' AND tgdeferrable AND tginitdeferred)
  THEN RAISE EXCEPTION 'runtime allocation/session deferred binding missing'; END IF;
  SELECT pg_get_functiondef('agent.enforce_runtime_allocation_lifecycle()'::regprocedure) INTO allocation_lifecycle;
  IF position('runtime host capacity unavailable' IN allocation_lifecycle)=0
    OR position('runtime host capacity release conflict' IN allocation_lifecycle)=0
  THEN RAISE EXCEPTION 'runtime capacity accounting trigger incomplete'; END IF;
  IF EXISTS (
    SELECT 1 FROM information_schema.routine_privileges
    WHERE routine_schema='agent' AND routine_name LIKE 'runtime_%_host' AND grantee='PUBLIC' AND privilege_type='EXECUTE'
  ) THEN RAISE EXCEPTION 'runtime host control functions are public'; END IF;
END
$verify$;
ROLLBACK;
SELECT json_build_object('status','passed','schema_version',30,'runtime_host_capacity','durable_fenced_event_bound') AS current_schema_verification;
