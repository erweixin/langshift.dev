\set ON_ERROR_STOP on
BEGIN;
DO $verify$
DECLARE actual integer; definition text;
BEGIN
  SELECT count(*) INTO actual FROM public.lites_schema_migrations;
  IF actual<>19 OR NOT EXISTS (SELECT 1 FROM public.lites_schema_migrations WHERE version=19 AND name='approval_scope_drift') THEN RAISE EXCEPTION 'expected migration version 19'; END IF;
  IF to_regprocedure('agent.list_stale_approval_tenants(uuid,uuid,integer,integer,integer)') IS NULL THEN RAISE EXCEPTION 'approval scope drift scan missing'; END IF;
  SELECT pg_get_functiondef('agent.list_stale_approval_tenants(uuid,uuid,integer,integer,integer)'::regprocedure) INTO definition;
  IF position('a.status=''pending''' IN definition)=0 OR position('tool_call_version<>a.target_version' IN definition)=0 OR position('run_version<>a.target_version' IN definition)=0 OR position('permission_snapshot<>format' IN definition)=0 THEN RAISE EXCEPTION 'approval scope drift scan incomplete'; END IF;
END
$verify$;
ROLLBACK;
SELECT json_build_object('status','passed','schema_version',19,'approval_scope_drift','epoch_fenced_sharded_pending_only') AS current_schema_verification;
