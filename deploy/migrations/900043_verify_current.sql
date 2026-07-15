BEGIN;

DO $verify$
BEGIN
  IF (SELECT count(*) FROM public.lites_schema_migrations)<>43
    OR NOT EXISTS (SELECT 1 FROM public.lites_schema_migrations WHERE version=43 AND name='child_run_orchestration_contract')
  THEN RAISE EXCEPTION 'expected migration version 43'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='runs_orchestration_identity_contract')
    OR NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='child_groups_join_contract')
    OR NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='child_group_members_child_unique')
    OR NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='runs_initialize_root_orchestration_identity' AND tgenabled='O')
    OR NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='runs_orchestration_identity_immutable' AND tgenabled='O')
    OR NOT EXISTS (SELECT 1 FROM pg_policies WHERE schemaname='agent' AND tablename='child_group_members')
    OR NOT EXISTS (SELECT 1 FROM pg_policies WHERE schemaname='agent' AND tablename='orchestration_quotas')
  THEN RAISE EXCEPTION 'child Run orchestration constraints are incomplete'; END IF;
END
$verify$;

INSERT INTO identity.users(id,normalized_email,locale,status)
VALUES('43000000-0000-4000-8000-000000000001','child-run-contract@example.invalid','en','active');
INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id)
VALUES('43000000-0000-4000-8000-000000000002','personal','Child Run Contract','active','US','43000000-0000-4000-8000-000000000001');
INSERT INTO agent.runs(id,tenant_id,user_id,conversation_id,status,run_version,due_at,profile_snapshot_id,budget_snapshot)
VALUES('43000000-0000-4000-8000-000000000003','43000000-0000-4000-8000-000000000002','43000000-0000-4000-8000-000000000001','43000000-0000-4000-8000-000000000004','accepted',1,CURRENT_TIMESTAMP+interval '1 hour','child-contract@v1','{}');
INSERT INTO agent.tool_calls(id,tenant_id,user_id,run_id,status,tool_call_version,tool_name,descriptor_snapshot_id,normalized_input_ref,request_hash,effect_class,execution_mode)
VALUES('43000000-0000-4000-8000-000000000005','43000000-0000-4000-8000-000000000002','43000000-0000-4000-8000-000000000001','43000000-0000-4000-8000-000000000003','succeeded',1,'spawn_agent_run','spawn_agent_run@v1','encrypted://child-contract/input','child-contract-request','read_only','inline_platform');
INSERT INTO agent.child_groups(id,tenant_id,parent_run_id,join_policy,required_count,step_id,quorum_count,continuation_kind)
VALUES('43000000-0000-4000-8000-000000000006','43000000-0000-4000-8000-000000000002','43000000-0000-4000-8000-000000000003','all',1,'contract-probe',1,'resume_parent');
INSERT INTO agent.outbox(id,tenant_id,command_id,command_type,aggregate_kind,aggregate_id,store_epoch,payload_ref,payload_hash,status,available_at)
VALUES('43000000-0000-4000-8000-000000000009','43000000-0000-4000-8000-000000000002','43000000-0000-4000-8000-000000000008','StartAgentRun','run','43000000-0000-4000-8000-000000000007','43000000-0000-4000-8000-000000000010','encrypted://child-contract/start','child-contract-start','pending',CURRENT_TIMESTAMP);
INSERT INTO agent.jobs(id,tenant_id,command_id,queue_class,resource_class,priority,cost_units,max_attempts,status,available_at,due_at,enqueued_at)
VALUES('43000000-0000-4000-8000-000000000011','43000000-0000-4000-8000-000000000002','43000000-0000-4000-8000-000000000008','interactive','llm',100,1,5,'pending',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP+interval '30 minutes',CURRENT_TIMESTAMP);
INSERT INTO agent.runs(id,tenant_id,user_id,conversation_id,status,run_version,pending_command_id,due_at,profile_snapshot_id,budget_snapshot,parent_run_id,root_run_id,spawn_tool_call_id,child_group_id,depth,inherited_budget_microunits)
VALUES('43000000-0000-4000-8000-000000000007','43000000-0000-4000-8000-000000000002','43000000-0000-4000-8000-000000000001','43000000-0000-4000-8000-000000000004','queued',2,'43000000-0000-4000-8000-000000000008',CURRENT_TIMESTAMP+interval '30 minutes','child-contract@v1','{}','43000000-0000-4000-8000-000000000003','43000000-0000-4000-8000-000000000003','43000000-0000-4000-8000-000000000005','43000000-0000-4000-8000-000000000006',1,1000);
INSERT INTO agent.child_group_members(tenant_id,group_id,child_run_id,required)
VALUES('43000000-0000-4000-8000-000000000002','43000000-0000-4000-8000-000000000006','43000000-0000-4000-8000-000000000007',true);
INSERT INTO agent.orchestration_quotas(tenant_id,root_run_id,total_descendants,concurrent_children,allocated_budget_microunits)
VALUES('43000000-0000-4000-8000-000000000002','43000000-0000-4000-8000-000000000003',1,1,1000);
SET CONSTRAINTS ALL IMMEDIATE;

DO $probe$
DECLARE root_identity uuid; members integer; descendants integer;
BEGIN
  SELECT root_run_id INTO root_identity FROM agent.runs WHERE id='43000000-0000-4000-8000-000000000003';
  SELECT count(*) INTO members FROM agent.child_group_members WHERE group_id='43000000-0000-4000-8000-000000000006';
  SELECT total_descendants INTO descendants FROM agent.orchestration_quotas WHERE root_run_id='43000000-0000-4000-8000-000000000003';
  IF root_identity<>'43000000-0000-4000-8000-000000000003' OR members<>1 OR descendants<>1 THEN
    RAISE EXCEPTION 'child Run protocol probe did not converge';
  END IF;
END
$probe$;

ROLLBACK;
SELECT json_build_object(
  'status','passed','schema_version',43,
  'child_run_orchestration','tree_identity_member_join_and_root_quota_fenced'
) AS current_schema_verification;
