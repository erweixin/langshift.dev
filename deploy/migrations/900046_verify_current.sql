BEGIN;

DO $verify$
BEGIN
  IF (SELECT count(*) FROM public.lites_schema_migrations)<>46
    OR NOT EXISTS (SELECT 1 FROM public.lites_schema_migrations WHERE version=46 AND name='scheduler_capacity_and_dispatch_fence')
  THEN RAISE EXCEPTION 'expected migration version 46'; END IF;
  IF to_regprocedure('agent.scheduler_active_counts(text,integer)') IS NULL
    OR to_regprocedure('agent.scheduler_list_ready_jobs_v2(text,text,bigint,bytea,uuid,timestamp with time zone,integer)') IS NULL
  THEN RAISE EXCEPTION 'scheduler capacity or dispatch-fence capability missing'; END IF;
  IF EXISTS (
    SELECT 1 FROM information_schema.routine_privileges
    WHERE routine_schema='agent' AND routine_name IN ('scheduler_active_counts','scheduler_list_ready_jobs_v2')
      AND grantee='PUBLIC' AND privilege_type='EXECUTE'
  ) THEN RAISE EXCEPTION 'scheduler capability is public'; END IF;
END
$verify$;

ROLLBACK;
SELECT json_build_object('status','passed','schema_version',46,'scheduler_active_counts','durable','dispatch_fence','queue_generation_and_dispatch_version') AS current_schema_verification;
