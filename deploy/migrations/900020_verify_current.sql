\set ON_ERROR_STOP on
BEGIN;
DO $verify$
DECLARE actual integer; lifecycle text;
BEGIN
  SELECT count(*) INTO actual FROM public.lites_schema_migrations;
  IF actual<>20 OR NOT EXISTS (SELECT 1 FROM public.lites_schema_migrations WHERE version=20 AND name='workspace_revision_protocol') THEN RAISE EXCEPTION 'expected migration version 20'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='agent.workspace_revision_commits'::regclass AND conname='workspace_revision_authorization_contract') THEN RAISE EXCEPTION 'workspace authorization contract missing'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='agent.workspace_revision_commits'::regclass AND conname='workspace_revision_publish_contract') THEN RAISE EXCEPTION 'workspace publish contract missing'; END IF;
  SELECT pg_get_functiondef('agent.enforce_workspace_revision_lifecycle()'::regprocedure) INTO lifecycle;
  IF position('workspace publish fence must advance' IN lifecycle)=0 OR position('immutable workspace revision scope mutation is forbidden' IN lifecycle)=0 THEN RAISE EXCEPTION 'workspace lifecycle trigger incomplete'; END IF;
  IF to_regprocedure('agent.list_workspace_recovery_tenants(uuid,uuid,integer,integer,integer,timestamp with time zone)') IS NULL THEN RAISE EXCEPTION 'workspace recovery scan missing'; END IF;
END
$verify$;
ROLLBACK;
SELECT json_build_object('status','passed','schema_version',20,'workspace_revision_protocol','approval_bound_fenced_cas_recoverable') AS current_schema_verification;
