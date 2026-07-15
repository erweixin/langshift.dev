\set ON_ERROR_STOP on
BEGIN;
DO $verify$
DECLARE actual integer; bucket_lifecycle text; reservation_lifecycle text;
BEGIN
  SELECT count(*) INTO actual FROM public.lites_schema_migrations;
  IF actual<>27 OR NOT EXISTS (SELECT 1 FROM public.lites_schema_migrations WHERE version=27 AND name='usage_accounting_protocol') THEN RAISE EXCEPTION 'expected migration version 27'; END IF;
  SELECT pg_get_functiondef('contracts.enforce_credit_bucket_accounting()'::regprocedure) INTO bucket_lifecycle;
  SELECT pg_get_functiondef('contracts.enforce_usage_reservation_lifecycle()'::regprocedure) INTO reservation_lifecycle;
  IF position('immutable credit bucket scope mutation is forbidden' IN bucket_lifecycle)=0 THEN RAISE EXCEPTION 'credit bucket accounting boundary is absent'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_proc WHERE oid='contracts.reserve_credit_units(uuid,uuid,bigint,timestamptz,timestamptz)'::regprocedure AND prosecdef) THEN RAISE EXCEPTION 'privileged credit reservation boundary is absent'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_proc WHERE oid='contracts.settle_credit_units(uuid,uuid,bigint,bigint,timestamptz)'::regprocedure AND prosecdef) THEN RAISE EXCEPTION 'privileged credit settlement boundary is absent'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_proc WHERE oid='contracts.release_credit_units(uuid,uuid,bigint,timestamptz)'::regprocedure AND prosecdef) THEN RAISE EXCEPTION 'privileged credit release boundary is absent'; END IF;
  IF position('terminal usage reservation mutation is forbidden' IN reservation_lifecycle)=0 THEN RAISE EXCEPTION 'usage reservation terminality is absent'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='llm_provider_attempts_usage_reservation_fk') THEN RAISE EXCEPTION 'provider attempt reservation binding is absent'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='llm_provider_attempts_usage_binding' AND NOT tgisinternal) THEN RAISE EXCEPTION 'provider attempt reservation immutability is absent'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='provider_costs_attempt_fk') THEN RAISE EXCEPTION 'provider cost physical attempt binding is absent'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_proc WHERE oid='contracts.list_releasable_usage_tenants(uuid,uuid,integer,integer,integer,timestamptz)'::regprocedure AND prosecdef) THEN RAISE EXCEPTION 'RLS-safe usage release discovery is absent'; END IF;
END
$verify$;
ROLLBACK;
SELECT json_build_object('status','passed','schema_version',27,'usage_accounting','hard_cap_reserve_settle_release_provider_cost') AS current_schema_verification;
