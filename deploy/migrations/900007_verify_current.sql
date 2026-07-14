\set ON_ERROR_STOP on

BEGIN;

DO $verify_current$
DECLARE
  actual integer;
BEGIN
  SELECT count(*) INTO actual FROM public.lites_schema_migrations;
  IF actual<>7 OR NOT EXISTS (SELECT 1 FROM public.lites_schema_migrations WHERE version=7 AND name='parallel_group_contract') THEN
    RAISE EXCEPTION 'expected migration version 7, found % rows',actual;
  END IF;
  SELECT count(*) INTO actual FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
    WHERE c.relkind='r' AND n.nspname IN ('identity','product','agent','contracts');
  IF actual<>94 THEN RAISE EXCEPTION 'expected 94 current contract tables, found %',actual; END IF;
  SELECT count(*) INTO actual FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
    WHERE c.relkind='r' AND n.nspname IN ('identity','product','agent','contracts') AND c.relrowsecurity AND c.relforcerowsecurity;
  IF actual<>86 THEN RAISE EXCEPTION 'expected 86 current forced-RLS tables, found %',actual; END IF;
  IF to_regclass('agent.parallel_group_members') IS NULL THEN RAISE EXCEPTION 'parallel group membership table missing'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='agent.parallel_groups'::regclass AND conname='parallel_groups_join_contract') THEN RAISE EXCEPTION 'parallel group join contract missing'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid='agent.parallel_groups'::regclass AND tgname='parallel_groups_membership_complete' AND tgdeferrable AND tginitdeferred) THEN RAISE EXCEPTION 'parallel group deferred completeness trigger missing'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid='agent.parallel_group_members'::regclass AND tgname='parallel_group_members_membership_complete' AND tgdeferrable AND tginitdeferred) THEN RAISE EXCEPTION 'parallel member deferred completeness trigger missing'; END IF;
  IF to_regclass('agent.scheduler_resources') IS NULL THEN RAISE EXCEPTION 'scheduler resource state missing'; END IF;
  IF EXISTS (SELECT 1 FROM information_schema.routine_privileges WHERE routine_schema='agent' AND routine_name LIKE 'scheduler_%' AND grantee='PUBLIC' AND privilege_type='EXECUTE') THEN RAISE EXCEPTION 'scheduler capability is public'; END IF;
END
$verify_current$;

INSERT INTO identity.users(id,normalized_email,locale,status)
VALUES('71000000-0000-4000-8000-000000000001','v7-probe@lites.invalid','en','active');
INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id)
VALUES('71000000-0000-4000-8000-000000000002','personal','V7 Probe','active','US','71000000-0000-4000-8000-000000000001');
INSERT INTO agent.runs(id,tenant_id,user_id,conversation_id,status,run_version,due_at,profile_snapshot_id,budget_snapshot)
VALUES('71000000-0000-4000-8000-000000000003','71000000-0000-4000-8000-000000000002','71000000-0000-4000-8000-000000000001','71000000-0000-4000-8000-000000000004','accepted',1,CURRENT_TIMESTAMP+interval '1 hour','probe@sha256:v7','{}');
INSERT INTO agent.tool_calls(id,tenant_id,user_id,run_id,status,tool_call_version,tool_name,descriptor_snapshot_id,normalized_input_ref,request_hash,effect_class)
VALUES
('71000000-0000-4000-8000-000000000005','71000000-0000-4000-8000-000000000002','71000000-0000-4000-8000-000000000001','71000000-0000-4000-8000-000000000003','succeeded',1,'probe_one','probe@v1','encrypted://probe/one','hash-one','read_only'),
('71000000-0000-4000-8000-000000000006','71000000-0000-4000-8000-000000000002','71000000-0000-4000-8000-000000000001','71000000-0000-4000-8000-000000000003','succeeded',1,'probe_two','probe@v1','encrypted://probe/two','hash-two','read_only');
INSERT INTO agent.parallel_groups(id,tenant_id,run_id,join_policy,required_count,group_kind,step_id,quorum_count,continuation_kind)
VALUES('71000000-0000-4000-8000-000000000007','71000000-0000-4000-8000-000000000002','71000000-0000-4000-8000-000000000003','all',2,'execution','valid-step',2,'resume');
INSERT INTO agent.parallel_group_members(tenant_id,group_id,tool_call_id,required)
VALUES
('71000000-0000-4000-8000-000000000002','71000000-0000-4000-8000-000000000007','71000000-0000-4000-8000-000000000005',true),
('71000000-0000-4000-8000-000000000002','71000000-0000-4000-8000-000000000007','71000000-0000-4000-8000-000000000006',true);
SET CONSTRAINTS ALL IMMEDIATE;
SET CONSTRAINTS ALL DEFERRED;

DO $verify_incomplete_group_rejected$
BEGIN
  BEGIN
    INSERT INTO agent.parallel_groups(id,tenant_id,run_id,join_policy,required_count,group_kind,step_id,quorum_count,continuation_kind)
    VALUES('71000000-0000-4000-8000-000000000008','71000000-0000-4000-8000-000000000002','71000000-0000-4000-8000-000000000003','all',2,'execution','invalid-step',2,'resume');
    INSERT INTO agent.parallel_group_members(tenant_id,group_id,tool_call_id,required)
    VALUES('71000000-0000-4000-8000-000000000002','71000000-0000-4000-8000-000000000008','71000000-0000-4000-8000-000000000005',true);
    SET CONSTRAINTS ALL IMMEDIATE;
    RAISE EXCEPTION 'incomplete parallel group unexpectedly committed';
  EXCEPTION WHEN check_violation THEN
    NULL;
  END;
END
$verify_incomplete_group_rejected$;

ROLLBACK;

SELECT json_build_object('status','passed','schema_version',7,'table_count',94,'forced_rls_count',86,'parallel_group_completeness','deferred') AS current_schema_verification;
