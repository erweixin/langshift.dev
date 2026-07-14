DROP FUNCTION IF EXISTS agent.list_stale_approval_tenants(uuid,uuid,integer,integer,integer);
DROP INDEX IF EXISTS agent.approvals_pending_scope_idx;
