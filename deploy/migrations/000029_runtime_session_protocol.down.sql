DROP POLICY IF EXISTS runtime_sessions_tenant_isolation ON agent.runtime_sessions;
DROP POLICY IF EXISTS runtime_policy_snapshots_tenant_isolation ON agent.runtime_policy_snapshots;
DROP FUNCTION IF EXISTS agent.list_runtime_recovery_tenants(uuid,uuid,integer,integer,integer,timestamptz);
DROP TRIGGER IF EXISTS runtime_session_event_guard ON agent.runtime_sessions;
DROP FUNCTION IF EXISTS agent.validate_runtime_session_event();
DROP TRIGGER IF EXISTS runtime_policy_event_guard ON agent.runtime_policy_snapshots;
DROP FUNCTION IF EXISTS agent.validate_runtime_policy_event();
DROP TRIGGER IF EXISTS runtime_session_lifecycle ON agent.runtime_sessions;
DROP FUNCTION IF EXISTS agent.enforce_runtime_session_lifecycle();
DROP TRIGGER IF EXISTS runtime_policy_snapshots_append_only ON agent.runtime_policy_snapshots;
DROP TABLE IF EXISTS agent.runtime_sessions;
DROP TABLE IF EXISTS agent.runtime_policy_snapshots;

