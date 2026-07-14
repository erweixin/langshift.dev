\set ON_ERROR_STOP on

BEGIN;

DO $verify_current$
DECLARE actual integer; lifecycle text;
BEGIN
  SELECT count(*) INTO actual FROM public.lites_schema_migrations;
  IF actual<>15 OR NOT EXISTS (SELECT 1 FROM public.lites_schema_migrations WHERE version=15 AND name='repair_recovery_scan') THEN
    RAISE EXCEPTION 'expected migration version 15, found % rows',actual;
  END IF;
  SELECT count(*) INTO actual FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
    WHERE c.relkind='r' AND n.nspname IN ('identity','product','agent','contracts');
  IF actual<>94 THEN RAISE EXCEPTION 'expected 94 current contract tables, found %',actual; END IF;
  SELECT count(*) INTO actual FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
    WHERE c.relkind='r' AND n.nspname IN ('identity','product','agent','contracts') AND c.relrowsecurity AND c.relforcerowsecurity;
  IF actual<>86 THEN RAISE EXCEPTION 'expected 86 current forced-RLS tables, found %',actual; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='agent.repair_commands'::regclass AND conname='repair_commands_target_tool_fk') THEN RAISE EXCEPTION 'repair target ownership missing'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='agent.repair_approvals'::regclass AND conname='repair_approvals_tenant_repair_fk') THEN RAISE EXCEPTION 'repair approval tenant ownership missing'; END IF;
  SELECT pg_get_functiondef('agent.enforce_repair_command_lifecycle()'::regprocedure) INTO lifecycle;
  IF position('terminal repair command mutation is forbidden' IN lifecycle)=0 OR position('NEW.version<>OLD.version+1' IN lifecycle)=0 THEN
    RAISE EXCEPTION 'repair lifecycle fence missing';
  END IF;
  IF to_regprocedure('agent.list_recoverable_repair_tenants(uuid,uuid,integer,integer,integer,timestamp with time zone)') IS NULL THEN RAISE EXCEPTION 'repair recovery tenant capability missing'; END IF;
END
$verify_current$;

ROLLBACK;

SELECT json_build_object('status','passed','schema_version',15,'table_count',94,'forced_rls_count',86,'repair_recovery','epoch_fenced') AS current_schema_verification;
