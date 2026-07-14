\set ON_ERROR_STOP on

BEGIN;

DO $verify_current$
DECLARE
  actual integer;
BEGIN
  SELECT count(*) INTO actual FROM public.lites_schema_migrations;
  IF actual<>6 OR NOT EXISTS (SELECT 1 FROM public.lites_schema_migrations WHERE version=6 AND name='scheduler_dispatch_protocol') THEN
    RAISE EXCEPTION 'expected migration version 6, found % rows', actual;
  END IF;

  SELECT count(*) INTO actual
  FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
  WHERE c.relkind='r' AND n.nspname IN ('identity','product','agent','contracts');
  IF actual<>93 THEN RAISE EXCEPTION 'expected 93 current contract tables, found %',actual; END IF;

  IF to_regclass('agent.scheduler_resources') IS NULL THEN RAISE EXCEPTION 'scheduler resource state missing'; END IF;
  IF to_regprocedure('agent.scheduler_claim_resource(text,text,bytea,timestamp with time zone,timestamp with time zone)') IS NULL THEN RAISE EXCEPTION 'scheduler resource claim capability missing'; END IF;
  IF to_regprocedure('agent.scheduler_list_ready_jobs(text,text,bigint,bytea,uuid,timestamp with time zone,integer)') IS NULL THEN RAISE EXCEPTION 'scheduler candidate capability missing'; END IF;
  IF to_regprocedure('agent.scheduler_commit_dispatch(text,text,bigint,bytea,jsonb,jsonb,timestamp with time zone,timestamp with time zone)') IS NULL THEN RAISE EXCEPTION 'scheduler commit capability missing'; END IF;
  IF EXISTS (SELECT 1 FROM information_schema.routine_privileges WHERE routine_schema='agent' AND routine_name LIKE 'scheduler_%' AND grantee='PUBLIC' AND privilege_type='EXECUTE') THEN RAISE EXCEPTION 'scheduler capability is public'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='agent.jobs'::regclass AND conname='jobs_dispatch_contract') THEN RAISE EXCEPTION 'job dispatch constraint missing'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='agent.runs'::regclass AND conname='runs_active_attempt_command_fk' AND contype='f') THEN RAISE EXCEPTION 'run execution ownership foreign key missing'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid='agent.job_attempts'::regclass AND tgname='job_attempts_append_only') THEN RAISE EXCEPTION 'job attempt lifecycle trigger missing'; END IF;
END
$verify_current$;

ROLLBACK;

SELECT json_build_object(
  'status','passed',
  'schema_version',6,
  'table_count',93,
  'scheduler_capabilities',4,
  'public_scheduler_capabilities',0
) AS current_schema_verification;
