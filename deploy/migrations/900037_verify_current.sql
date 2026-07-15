\set ON_ERROR_STOP on
BEGIN;
DO $verify$
BEGIN
  IF (SELECT count(*) FROM public.lites_schema_migrations)<>37 OR NOT EXISTS (
    SELECT 1 FROM public.lites_schema_migrations WHERE version=37 AND name='behavior_snapshot_promotion'
  ) THEN RAISE EXCEPTION 'expected migration version 37'; END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_proc
    WHERE oid='agent.enforce_behavior_channel_append()'::regprocedure
      AND prosecdef AND proconfig @> ARRAY['row_security=off']
  ) OR has_function_privilege('public','agent.enforce_behavior_channel_append()','EXECUTE')
  THEN RAISE EXCEPTION 'behavior channel append authority is not fenced'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_class c WHERE c.oid='agent.behavior_snapshots'::regclass AND c.relrowsecurity AND c.relforcerowsecurity)
    OR NOT EXISTS (SELECT 1 FROM pg_class c WHERE c.oid='agent.behavior_evaluation_reports'::regclass AND c.relrowsecurity AND c.relforcerowsecurity)
    OR NOT EXISTS (SELECT 1 FROM pg_class c WHERE c.oid='agent.behavior_channel_deployments'::regclass AND c.relrowsecurity AND c.relforcerowsecurity)
    OR NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid='agent.behavior_snapshots'::regclass AND tgname='behavior_snapshots_append_only')
    OR NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid='agent.behavior_evaluation_reports'::regclass AND tgname='behavior_evaluation_reports_append_only')
    OR NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid='agent.behavior_channel_deployments'::regclass AND tgname='behavior_channel_deployments_append_only')
    OR NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid='agent.behavior_channel_deployments'::regclass AND tgname='behavior_channel_deployments_insert')
    OR NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='agent.behavior_channel_deployments'::regclass AND conname='behavior_channel_scope_contract')
    OR NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='agent.behavior_channel_deployments'::regclass AND conname='behavior_channel_evaluation_fk')
    OR (SELECT count(*) FROM information_schema.columns WHERE table_schema='agent' AND table_name='behavior_channel_deployments'
      AND column_name IN ('rollback_hash','rollback_trigger','rollback_observed_value','rollback_threshold','automation_key_id','automation_signature','rollback_occurred_at'))<>7
  THEN RAISE EXCEPTION 'behavior snapshot promotion protocol is incomplete'; END IF;
END
$verify$;
ROLLBACK;
SELECT json_build_object('status','passed','schema_version',37,'behavior_promotion','immutable_signed_rollback') AS current_schema_verification;
