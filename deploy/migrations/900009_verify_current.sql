\set ON_ERROR_STOP on

BEGIN;

DO $verify_current$
DECLARE actual integer;
BEGIN
  SELECT count(*) INTO actual FROM public.lites_schema_migrations;
  IF actual<>9 OR NOT EXISTS (SELECT 1 FROM public.lites_schema_migrations WHERE version=9 AND name='tool_effect_ledger') THEN
    RAISE EXCEPTION 'expected migration version 9, found % rows',actual;
  END IF;
  SELECT count(*) INTO actual FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
    WHERE c.relkind='r' AND n.nspname IN ('identity','product','agent','contracts');
  IF actual<>94 THEN RAISE EXCEPTION 'expected 94 current contract tables, found %',actual; END IF;
  SELECT count(*) INTO actual FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
    WHERE c.relkind='r' AND n.nspname IN ('identity','product','agent','contracts') AND c.relrowsecurity AND c.relforcerowsecurity;
  IF actual<>86 THEN RAISE EXCEPTION 'expected 86 current forced-RLS tables, found %',actual; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='agent.tool_effects'::regclass AND conname='tool_effects_tenant_tool_run_fk') THEN RAISE EXCEPTION 'effect tool/run ownership missing'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='agent.tool_effects'::regclass AND conname='tool_effects_result_event_fk' AND condeferrable AND condeferred) THEN RAISE EXCEPTION 'effect result event is not deferred'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='agent.tool_effects'::regclass AND conname='tool_effects_tool_call_once') THEN RAISE EXCEPTION 'single effect per ToolCall missing'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid='agent.tool_effects'::regclass AND tgname='tool_effects_lifecycle') THEN RAISE EXCEPTION 'effect lifecycle trigger missing'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='agent.continuations'::regclass AND conname='continuations_group_once') THEN RAISE EXCEPTION 'continuation uniqueness missing'; END IF;
  IF to_regclass('agent.scheduler_resources') IS NULL THEN RAISE EXCEPTION 'scheduler resource state missing'; END IF;
END
$verify_current$;

INSERT INTO identity.users(id,normalized_email,locale,status)
VALUES('91000000-0000-4000-8000-000000000001','v9-probe@lites.invalid','en','active');
INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id)
VALUES('91000000-0000-4000-8000-000000000002','personal','V9 Probe','active','US','91000000-0000-4000-8000-000000000001');
INSERT INTO agent.runs(id,tenant_id,user_id,conversation_id,status,run_version,due_at,profile_snapshot_id,budget_snapshot)
VALUES('91000000-0000-4000-8000-000000000003','91000000-0000-4000-8000-000000000002','91000000-0000-4000-8000-000000000001','91000000-0000-4000-8000-000000000004','accepted',1,CURRENT_TIMESTAMP+interval '1 hour','probe@sha256:v9','{}');
INSERT INTO agent.tool_calls(id,tenant_id,user_id,run_id,status,tool_call_version,tool_name,descriptor_snapshot_id,normalized_input_ref,request_hash,effect_class,effect_key)
VALUES('91000000-0000-4000-8000-000000000005','91000000-0000-4000-8000-000000000002','91000000-0000-4000-8000-000000000001','91000000-0000-4000-8000-000000000003','succeeded',1,'probe_write','probe@v1','encrypted://probe/input','request-one','idempotent_write','effect-one');
INSERT INTO agent.tool_effects(id,tenant_id,effect_scope,provider_id,tool_name,effect_key,request_hash,status,tool_call_id,run_id,effect_class)
VALUES('91000000-0000-4000-8000-000000000006','91000000-0000-4000-8000-000000000002','tenant:probe','provider','probe_write','effect-one','request-one','prepared','91000000-0000-4000-8000-000000000005','91000000-0000-4000-8000-000000000003','idempotent_write');

DO $verify_effect_guards$
BEGIN
  BEGIN
    UPDATE agent.tool_effects SET request_hash='mutated',version=version+1 WHERE id='91000000-0000-4000-8000-000000000006';
  EXCEPTION WHEN raise_exception THEN NULL;
  END;
  IF EXISTS (SELECT 1 FROM agent.tool_effects WHERE id='91000000-0000-4000-8000-000000000006' AND request_hash='mutated') THEN RAISE EXCEPTION 'effect identity mutation unexpectedly succeeded'; END IF;
  BEGIN
    INSERT INTO agent.tool_effects(id,tenant_id,effect_scope,provider_id,tool_name,effect_key,request_hash,status,tool_call_id,run_id,effect_class)
    VALUES('91000000-0000-4000-8000-000000000007','91000000-0000-4000-8000-000000000002','tenant:probe','provider','probe_write','effect-two','request-two','prepared','91000000-0000-4000-8000-000000000005','91000000-0000-4000-8000-000000000003','read_only');
  EXCEPTION WHEN check_violation THEN NULL;
  END;
  IF EXISTS (SELECT 1 FROM agent.tool_effects WHERE id='91000000-0000-4000-8000-000000000007') THEN RAISE EXCEPTION 'read-only effect ledger unexpectedly succeeded'; END IF;
END
$verify_effect_guards$;

ROLLBACK;

SELECT json_build_object('status','passed','schema_version',9,'table_count',94,'forced_rls_count',86,'tool_effect_uniqueness','global_effect_key','effect_lifecycle','triggered') AS current_schema_verification;
