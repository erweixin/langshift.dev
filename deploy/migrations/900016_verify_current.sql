\set ON_ERROR_STOP on

BEGIN;

DO $verify_current$
DECLARE actual integer; lifecycle text; kind_contract text;
BEGIN
  SELECT count(*) INTO actual FROM public.lites_schema_migrations;
  IF actual<>16 OR NOT EXISTS (SELECT 1 FROM public.lites_schema_migrations WHERE version=16 AND name='generalized_repair_targets') THEN
    RAISE EXCEPTION 'expected migration version 16, found % rows',actual;
  END IF;
  SELECT count(*) INTO actual FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
    WHERE c.relkind='r' AND n.nspname IN ('identity','product','agent','contracts');
  IF actual<>94 THEN RAISE EXCEPTION 'expected 94 current contract tables, found %',actual; END IF;
  SELECT count(*) INTO actual FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
    WHERE c.relkind='r' AND n.nspname IN ('identity','product','agent','contracts') AND c.relrowsecurity AND c.relforcerowsecurity;
  IF actual<>86 THEN RAISE EXCEPTION 'expected 86 current forced-RLS tables, found %',actual; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='agent.repair_commands'::regclass AND conname='repair_commands_tool_call_fk') THEN RAISE EXCEPTION 'repair tool target ownership missing'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='agent.repair_commands'::regclass AND conname='repair_commands_anonymous_claim_fk') THEN RAISE EXCEPTION 'repair anonymous claim target ownership missing'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='identity.onboarding_claims'::regclass AND conname='onboarding_claims_tenant_id_id_unique') THEN RAISE EXCEPTION 'anonymous claim tenant identity missing'; END IF;
  SELECT pg_get_constraintdef(oid) INTO kind_contract FROM pg_constraint
    WHERE conrelid='agent.repair_commands'::regclass AND conname='repair_commands_kind_contract';
  IF position('anonymous_claim_reconciliation' IN kind_contract)=0 OR position('tool_effect_resolution' IN kind_contract)=0 THEN
    RAISE EXCEPTION 'generalized repair kind fence missing';
  END IF;
  SELECT pg_get_functiondef('agent.enforce_repair_command_lifecycle()'::regprocedure) INTO lifecycle;
  IF position('anonymous_claim_id' IN lifecycle)=0 OR position('source_tenant_id' IN lifecycle)=0 OR position('terminal repair command mutation is forbidden' IN lifecycle)=0 THEN
    RAISE EXCEPTION 'generalized repair immutability fence missing';
  END IF;
  IF to_regprocedure('agent.list_recoverable_repair_tenants(uuid,uuid,integer,integer,integer,timestamp with time zone)') IS NULL THEN RAISE EXCEPTION 'repair recovery tenant capability missing'; END IF;
END
$verify_current$;

ROLLBACK;

SELECT json_build_object('status','passed','schema_version',16,'table_count',94,'forced_rls_count',86,'repair_targets','tool_call_and_anonymous_claim') AS current_schema_verification;
