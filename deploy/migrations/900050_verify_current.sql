BEGIN;

DO $verify$
BEGIN
  IF (SELECT count(*) FROM public.lites_schema_migrations)<>50
    OR NOT EXISTS (SELECT 1 FROM public.lites_schema_migrations WHERE version=50 AND name='tool_execution_overlay')
  THEN RAISE EXCEPTION 'expected migration version 50'; END IF;
  IF to_regclass('agent.platform_tool_execution_overlays') IS NULL
    OR to_regclass('agent.tenant_tool_execution_overlays') IS NULL
    OR NOT EXISTS (SELECT 1 FROM pg_class WHERE oid='agent.tenant_tool_execution_overlays'::regclass AND relrowsecurity AND relforcerowsecurity)
    OR NOT EXISTS (SELECT 1 FROM pg_policies WHERE schemaname='agent' AND tablename='tenant_tool_execution_overlays' AND policyname='tenant_tool_execution_overlays_tenant_isolation')
    OR NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid='agent.platform_tool_execution_overlays'::regclass AND tgname='platform_tool_execution_overlays_append_only' AND tgenabled='O')
    OR NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid='agent.tenant_tool_execution_overlays'::regclass AND tgname='tenant_tool_execution_overlays_append_only' AND tgenabled='O')
    OR NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='platform_tool_overlay_scope_contract')
    OR NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='tenant_tool_overlay_scope_contract')
  THEN RAISE EXCEPTION 'tool execution overlay contract is incomplete'; END IF;
END
$verify$;

ROLLBACK;
SELECT json_build_object('status','passed','schema_version',50,'tool_execution_overlay','platform_and_tenant_deny_only_chain') AS current_schema_verification;
