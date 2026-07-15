DROP POLICY IF EXISTS runtime_executions_tenant_isolation ON agent.runtime_executions;
DROP TRIGGER IF EXISTS runtime_session_execution_settlement_guard ON agent.runtime_sessions;
DROP FUNCTION IF EXISTS agent.validate_runtime_session_execution_settlement();
DROP TRIGGER IF EXISTS runtime_execution_event_guard ON agent.runtime_executions;
DROP FUNCTION IF EXISTS agent.validate_runtime_execution_events();
DROP TRIGGER IF EXISTS runtime_execution_lifecycle ON agent.runtime_executions;
DROP FUNCTION IF EXISTS agent.enforce_runtime_execution_lifecycle();
DROP TABLE IF EXISTS agent.runtime_executions;
