\set ON_ERROR_STOP on
BEGIN;
DO $verify$
DECLARE actual integer; lifecycle text;
BEGIN
  SELECT count(*) INTO actual FROM public.lites_schema_migrations;
  IF actual<>23 OR NOT EXISTS (SELECT 1 FROM public.lites_schema_migrations WHERE version=23 AND name='workspace_reconciliation_lifecycle') THEN RAISE EXCEPTION 'expected migration version 23'; END IF;
  SELECT pg_get_functiondef('agent.enforce_workspace_revision_lifecycle()'::regprocedure) INTO lifecycle;
  IF position($needle$OLD.status='outcome_unknown' AND NEW.status='outcome_unknown'$needle$ IN lifecycle)=0 THEN RAISE EXCEPTION 'workspace reconciliation deferral transition is absent'; END IF;
  IF position($needle$NEW.reconciliation_attempts<>OLD.reconciliation_attempts+1$needle$ IN lifecycle)=0 THEN RAISE EXCEPTION 'workspace reconciliation attempt is not monotonic'; END IF;
END
$verify$;
ROLLBACK;
SELECT json_build_object('status','passed','schema_version',23,'workspace_reconciliation','durable_deferral_and_terminal_resolution') AS current_schema_verification;
