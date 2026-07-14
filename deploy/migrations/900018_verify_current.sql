\set ON_ERROR_STOP on
BEGIN;
DO $verify$
DECLARE actual integer; lifecycle text;
BEGIN
  SELECT count(*) INTO actual FROM public.lites_schema_migrations;
  IF actual<>18 OR NOT EXISTS (SELECT 1 FROM public.lites_schema_migrations WHERE version=18 AND name='approval_control_plane') THEN RAISE EXCEPTION 'expected migration version 18'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='agent.approvals'::regclass AND conname='approvals_lifecycle_contract') THEN RAISE EXCEPTION 'approval lifecycle constraint missing'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='agent.approval_decisions'::regclass AND conname='approval_decisions_event_fk') THEN RAISE EXCEPTION 'approval decision event binding missing'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='agent.approval_decisions'::regclass AND conname='approval_decisions_mode_contract') THEN RAISE EXCEPTION 'approval decision authorization mode missing'; END IF;
  SELECT pg_get_functiondef('agent.enforce_approval_lifecycle()'::regprocedure) INTO lifecycle;
  IF position('immutable approval scope mutation is forbidden' IN lifecycle)=0 OR position('NEW.version<>OLD.version+1' IN lifecycle)=0 THEN RAISE EXCEPTION 'approval lifecycle trigger incomplete'; END IF;
  IF to_regprocedure('agent.list_expired_approval_tenants(uuid,uuid,integer,integer,integer,timestamp with time zone)') IS NULL THEN RAISE EXCEPTION 'approval expiry scan missing'; END IF;
END
$verify$;
ROLLBACK;
SELECT json_build_object('status','passed','schema_version',18,'approval_control_plane','immutable_scope_event_bound_epoch_fenced') AS current_schema_verification;
