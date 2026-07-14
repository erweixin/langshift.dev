\set ON_ERROR_STOP on

BEGIN;

DO $verify_current$
DECLARE actual integer; lifecycle text; scheduler_definition text;
BEGIN
  SELECT count(*) INTO actual FROM public.lites_schema_migrations;
  IF actual<>12 OR NOT EXISTS (SELECT 1 FROM public.lites_schema_migrations WHERE version=12 AND name='command_redelivery_protocol') THEN
    RAISE EXCEPTION 'expected migration version 12, found % rows',actual;
  END IF;
  SELECT count(*) INTO actual FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
    WHERE c.relkind='r' AND n.nspname IN ('identity','product','agent','contracts');
  IF actual<>94 THEN RAISE EXCEPTION 'expected 94 current contract tables, found %',actual; END IF;
  SELECT count(*) INTO actual FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
    WHERE c.relkind='r' AND n.nspname IN ('identity','product','agent','contracts') AND c.relrowsecurity AND c.relforcerowsecurity;
  IF actual<>86 THEN RAISE EXCEPTION 'expected 86 current forced-RLS tables, found %',actual; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='agent.tool_effects'::regclass AND conname='tool_effects_tenant_tool_run_fk') THEN RAISE EXCEPTION 'effect tool/run ownership missing'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='agent.tool_effects'::regclass AND conname='tool_effects_result_event_fk' AND condeferrable AND condeferred) THEN RAISE EXCEPTION 'effect result event is not deferred'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='agent.tool_effects'::regclass AND conname='tool_effects_tool_call_once') THEN RAISE EXCEPTION 'single effect per ToolCall missing'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid='agent.tool_effects'::regclass AND tgname='tool_effects_lifecycle') THEN RAISE EXCEPTION 'effect lifecycle trigger missing'; END IF;
  SELECT pg_get_functiondef('agent.enforce_tool_effect_lifecycle()'::regprocedure) INTO lifecycle;
  IF position('OLD.status=''outcome_unknown'' AND NEW.status IN (''confirmed'',''failed'',''commit_authorized'',''accepted_unknown'')' IN lifecycle)=0 THEN
    RAISE EXCEPTION 'effect result evidence cannot be replaced during reconciliation';
  END IF;
  IF position('OLD.status=''outcome_unknown'' AND NEW.status=''outcome_unknown''' IN lifecycle)=0 OR position('NEW.reconciliation_due_at>OLD.reconciliation_due_at' IN lifecycle)=0 THEN
    RAISE EXCEPTION 'effect reconciliation retry cannot advance its deadline';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='agent.continuations'::regclass AND conname='continuations_group_once') THEN RAISE EXCEPTION 'continuation uniqueness missing'; END IF;
  IF to_regclass('agent.scheduler_resources') IS NULL THEN RAISE EXCEPTION 'scheduler resource state missing'; END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='agent' AND table_name='jobs' AND column_name='delivery_due_at') THEN RAISE EXCEPTION 'job delivery deadline missing'; END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='agent' AND table_name='jobs' AND column_name='queue_generation') THEN RAISE EXCEPTION 'job queue generation missing'; END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='agent' AND table_name='jobs' AND column_name='redelivery_count') THEN RAISE EXCEPTION 'job redelivery count missing'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='jobs_redelivery_contract' AND conrelid='agent.jobs'::regclass) THEN RAISE EXCEPTION 'job redelivery contract missing'; END IF;
  IF to_regprocedure('agent.list_command_reconciliation_tenants(uuid,uuid,integer,integer,integer,timestamp with time zone,timestamp with time zone)') IS NULL THEN RAISE EXCEPTION 'command reconciliation tenant capability missing'; END IF;
  IF to_regprocedure('agent.scheduler_list_ready_jobs(text,text,bigint,bytea,uuid,timestamp with time zone,integer)') IS NULL THEN RAISE EXCEPTION 'redelivery-aware scheduler candidate capability missing'; END IF;
  IF to_regprocedure('agent.scheduler_commit_dispatch(text,text,bigint,bytea,jsonb,jsonb,timestamp with time zone,timestamp with time zone)') IS NULL THEN RAISE EXCEPTION 'redelivery-aware scheduler commit capability missing'; END IF;
  SELECT pg_get_functiondef('agent.scheduler_list_ready_jobs(text,text,bigint,bytea,uuid,timestamp with time zone,integer)'::regprocedure) INTO scheduler_definition;
  IF position('redelivery_requested_at' IN scheduler_definition)=0 OR position('status IN (''pending'',''running'')' IN scheduler_definition)=0 THEN
    RAISE EXCEPTION 'scheduler does not expose explicit running-job redelivery';
  END IF;
END
$verify_current$;

ROLLBACK;

SELECT json_build_object('status','passed','schema_version',12,'table_count',94,'forced_rls_count',86,'command_redelivery','same_command_generation_fenced') AS current_schema_verification;
