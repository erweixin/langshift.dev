DROP FUNCTION IF EXISTS agent.list_expired_tool_effect_tenants(uuid,uuid,integer,integer,integer,timestamptz);
DROP INDEX IF EXISTS agent.tool_calls_expired_effect_idx;
