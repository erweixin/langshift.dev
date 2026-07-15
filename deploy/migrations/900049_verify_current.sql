BEGIN;

DO $verify$
DECLARE kind_definition text;
DECLARE continuation_definition text;
BEGIN
  IF (SELECT count(*) FROM public.lites_schema_migrations)<>49
    OR NOT EXISTS (SELECT 1 FROM public.lites_schema_migrations WHERE version=49 AND name='direct_tool_approval_protocol')
  THEN RAISE EXCEPTION 'expected migration version 49'; END IF;
  IF to_regclass('agent.tool_proposals') IS NULL
    OR NOT EXISTS (SELECT 1 FROM pg_policies WHERE schemaname='agent' AND tablename='tool_proposals' AND policyname='tool_proposals_tenant_isolation')
    OR NOT EXISTS (SELECT 1 FROM pg_class WHERE oid='agent.tool_proposals'::regclass AND relrowsecurity AND relforcerowsecurity)
    OR NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid='agent.tool_proposals'::regclass AND tgname='tool_proposals_append_only' AND tgenabled='O')
    OR NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='tool_proposals_scope_contract')
    OR NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='tool_proposals_tool_once')
    OR NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='tool_proposals_approval_once')
  THEN RAISE EXCEPTION 'direct tool proposal contract is incomplete'; END IF;
  SELECT pg_get_constraintdef(oid) INTO kind_definition
    FROM pg_constraint WHERE conname='parallel_groups_kind_contract';
  SELECT pg_get_constraintdef(oid) INTO continuation_definition
    FROM pg_constraint WHERE conname='parallel_groups_continuation_contract';
  IF position('approval_direct' IN kind_definition)=0
    OR position('approval_direct' IN continuation_definition)=0
  THEN RAISE EXCEPTION 'direct approval group contract is incomplete'; END IF;
END
$verify$;

ROLLBACK;
SELECT json_build_object('status','passed','schema_version',49,'direct_tool_approval','immutable_proposal_and_group_protocol') AS current_schema_verification;
