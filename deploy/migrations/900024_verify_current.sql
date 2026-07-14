\set ON_ERROR_STOP on
BEGIN;
DO $verify$
DECLARE actual integer; lifecycle text;
BEGIN
  SELECT count(*) INTO actual FROM public.lites_schema_migrations;
  IF actual<>24 OR NOT EXISTS (SELECT 1 FROM public.lites_schema_migrations WHERE version=24 AND name='artifact_revision_protocol') THEN RAISE EXCEPTION 'expected migration version 24'; END IF;
  SELECT pg_get_functiondef('product.enforce_artifact_lifecycle()'::regprocedure) INTO lifecycle;
  IF position('NEW.current_revision=OLD.current_revision+1' IN lifecycle)=0 THEN RAISE EXCEPTION 'artifact revision CAS transition is absent'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='artifact_revision_evidence_exact_fk') THEN RAISE EXCEPTION 'exact evidence binding is absent'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='artifact_revisions_created_event_fk') THEN RAISE EXCEPTION 'artifact revision event binding is absent'; END IF;
END
$verify$;
ROLLBACK;
SELECT json_build_object('status','passed','schema_version',24,'artifact_revisions','immutable_scanned_content_with_exact_evidence') AS current_schema_verification;
