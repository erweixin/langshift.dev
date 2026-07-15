\set ON_ERROR_STOP on
BEGIN;
DO $verify$
BEGIN
  IF (SELECT count(*) FROM public.lites_schema_migrations)<>38 OR NOT EXISTS (
    SELECT 1 FROM public.lites_schema_migrations WHERE version=38 AND name='run_behavior_binding'
  ) THEN RAISE EXCEPTION 'expected migration version 38'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='agent.runs'::regclass AND conname='runs_behavior_binding_contract')
    OR NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='agent.runs'::regclass AND conname='runs_behavior_deployment_fk')
    OR NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='agent.behavior_channel_deployments'::regclass AND conname='behavior_channel_run_binding_unique')
    OR (SELECT count(*) FROM information_schema.columns WHERE table_schema='agent' AND table_name='runs'
      AND column_name IN ('behavior_profile','behavior_environment','behavior_channel_id','behavior_channel_sequence'))<>4
  THEN RAISE EXCEPTION 'Run behavior binding is incomplete'; END IF;
END
$verify$;
ROLLBACK;
SELECT json_build_object('status','passed','schema_version',38,'run_behavior_binding','deployment_fk') AS current_schema_verification;
