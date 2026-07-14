\set ON_ERROR_STOP on

BEGIN;

DO $verify_current$
DECLARE actual integer; lifecycle text; scheduler_definition text;
BEGIN
  SELECT count(*) INTO actual FROM public.lites_schema_migrations;
  IF actual<>13 OR NOT EXISTS (SELECT 1 FROM public.lites_schema_migrations WHERE version=13 AND name='expired_effect_sweeper') THEN
    RAISE EXCEPTION 'expected migration version 13, found % rows',actual;
  END IF;
  SELECT count(*) INTO actual FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
    WHERE c.relkind='r' AND n.nspname IN ('identity','product','agent','contracts');
  IF actual<>94 THEN RAISE EXCEPTION 'expected 94 current contract tables, found %',actual; END IF;
  SELECT count(*) INTO actual FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
    WHERE c.relkind='r' AND n.nspname IN ('identity','product','agent','contracts') AND c.relrowsecurity AND c.relforcerowsecurity;
  IF actual<>86 THEN RAISE EXCEPTION 'expected 86 current forced-RLS tables, found %',actual; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='agent.tool_effects'::regclass AND conname='tool_effects_tenant_tool_run_fk') THEN RAISE EXCEPTION 'effect tool/run ownership missing'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='agent.tool_effects'::regclass AND conname='tool_effects_result_event_fk' AND condeferrable AND condeferred) THEN RAISE EXCEPTION 'effect result event is not deferred'; END IF;
  SELECT pg_get_functiondef('agent.enforce_tool_effect_lifecycle()'::regprocedure) INTO lifecycle;
  IF position('OLD.status=''outcome_unknown'' AND NEW.status=''outcome_unknown''' IN lifecycle)=0 OR position('NEW.reconciliation_due_at>OLD.reconciliation_due_at' IN lifecycle)=0 THEN
    RAISE EXCEPTION 'effect reconciliation retry cannot advance its deadline';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='agent.jobs'::regclass AND conname='jobs_redelivery_contract') THEN RAISE EXCEPTION 'job redelivery contract missing'; END IF;
  IF to_regprocedure('agent.list_command_reconciliation_tenants(uuid,uuid,integer,integer,integer,timestamp with time zone,timestamp with time zone)') IS NULL THEN RAISE EXCEPTION 'command reconciliation tenant capability missing'; END IF;
  IF to_regprocedure('agent.list_expired_tool_effect_tenants(uuid,uuid,integer,integer,integer,timestamp with time zone)') IS NULL THEN RAISE EXCEPTION 'expired effect tenant capability missing'; END IF;
  IF to_regclass('agent.tool_calls_expired_effect_idx') IS NULL THEN RAISE EXCEPTION 'expired effect index missing'; END IF;
  SELECT pg_get_functiondef('agent.scheduler_list_ready_jobs(text,text,bigint,bytea,uuid,timestamp with time zone,integer)'::regprocedure) INTO scheduler_definition;
  IF position('redelivery_requested_at' IN scheduler_definition)=0 THEN RAISE EXCEPTION 'scheduler redelivery protocol missing'; END IF;
END
$verify_current$;

ROLLBACK;

SELECT json_build_object('status','passed','schema_version',13,'table_count',94,'forced_rls_count',86,'expired_effect_sweeper','fenced_outcome_unknown') AS current_schema_verification;
