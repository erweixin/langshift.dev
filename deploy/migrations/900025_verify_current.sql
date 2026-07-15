\set ON_ERROR_STOP on
BEGIN;
DO $verify$
DECLARE actual integer; lifecycle text;
BEGIN
  SELECT count(*) INTO actual FROM public.lites_schema_migrations;
  IF actual<>25 OR NOT EXISTS (SELECT 1 FROM public.lites_schema_migrations WHERE version=25 AND name='portfolio_export_protocol') THEN RAISE EXCEPTION 'expected migration version 25'; END IF;
  SELECT pg_get_functiondef('product.enforce_portfolio_export_lifecycle()'::regprocedure) INTO lifecycle;
  IF position('immutable portfolio export scope mutation is forbidden' IN lifecycle)=0 THEN RAISE EXCEPTION 'portfolio manifest immutability is absent'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='portfolio_export_artifacts_revision_fk') THEN RAISE EXCEPTION 'exact artifact revision binding is absent'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='portfolio_export_artifacts_artifact_unique') THEN RAISE EXCEPTION 'one revision per artifact invariant is absent'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='portfolio_export_evidence_exact_fk') THEN RAISE EXCEPTION 'exact evidence binding is absent'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='portfolio_exports_start_command_fk') THEN RAISE EXCEPTION 'start command binding is absent'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_proc WHERE oid='agent.list_recoverable_portfolio_tenants(uuid,uuid,integer,integer,integer,timestamptz,timestamptz)'::regprocedure AND prosecdef) THEN RAISE EXCEPTION 'RLS-safe portfolio recovery discovery is absent'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_proc WHERE oid='agent.list_expirable_portfolio_tenants(uuid,uuid,integer,integer,integer,timestamptz)'::regprocedure AND prosecdef) THEN RAISE EXCEPTION 'RLS-safe portfolio expiry discovery is absent'; END IF;
END
$verify$;
ROLLBACK;
SELECT json_build_object('status','passed','schema_version',25,'portfolio_exports','run_bound_exact_revision_manifest_recoverable') AS current_schema_verification;
