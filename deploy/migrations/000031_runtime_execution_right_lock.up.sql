CREATE FUNCTION agent.runtime_lock_execution_right(
  p_tenant_id uuid,p_session_id uuid,p_approval_id uuid,p_approval_version bigint,p_now timestamptz
) RETURNS text
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,agent SET row_security=off AS $$
DECLARE approval_required boolean;
BEGIN
  IF p_tenant_id IS NULL OR p_session_id IS NULL OR p_now IS NULL THEN
    RAISE EXCEPTION 'invalid runtime execution-right arguments' USING ERRCODE='22023';
  END IF;

  SELECT p.approval_required INTO approval_required
  FROM agent.runtime_sessions s
  JOIN agent.runtime_policy_snapshots p
    ON p.tenant_id=s.tenant_id AND p.id=s.policy_snapshot_id
  WHERE s.tenant_id=p_tenant_id AND s.id=p_session_id AND s.status='requested';
  IF NOT FOUND THEN RETURN 'execution_invalid'; END IF;

  IF approval_required OR p_approval_id IS NOT NULL THEN
    IF p_approval_id IS NULL OR p_approval_version IS NULL OR p_approval_version<1 THEN RETURN 'approval_invalid'; END IF;
    PERFORM 1 FROM agent.approvals a
    WHERE a.tenant_id=p_tenant_id AND a.id=p_approval_id AND a.version=p_approval_version
      AND a.run_id=(SELECT s.run_id FROM agent.runtime_sessions s WHERE s.tenant_id=p_tenant_id AND s.id=p_session_id)
      AND a.tool_call_id=(SELECT s.tool_call_id FROM agent.runtime_sessions s WHERE s.tenant_id=p_tenant_id AND s.id=p_session_id)
      AND a.approval_kind='tool_execution' AND a.status='granted' AND a.expires_at>p_now
      AND EXISTS(SELECT 1 FROM agent.events e
        WHERE e.tenant_id=a.tenant_id AND e.aggregate_kind='approval' AND e.aggregate_id=a.id
          AND e.aggregate_version=a.version AND e.event_type='ApprovalGranted')
    FOR SHARE OF a;
    IF NOT FOUND THEN RETURN 'approval_invalid'; END IF;
  ELSIF p_approval_version IS NOT NULL THEN
    RETURN 'approval_invalid';
  END IF;

  PERFORM 1
  FROM agent.runtime_sessions s
  JOIN agent.tool_calls t
    ON t.tenant_id=s.tenant_id AND t.id=s.tool_call_id
  JOIN agent.job_attempts a
    ON a.tenant_id=s.tenant_id AND a.id=s.execution_attempt_id
  WHERE s.tenant_id=p_tenant_id AND s.id=p_session_id AND s.status='requested'
    AND t.user_id=s.user_id AND t.run_id=s.run_id
    AND t.status='executing' AND t.execution_mode='worker_runtime'
    AND t.active_command_id=s.command_id AND t.active_attempt_id=s.execution_attempt_id
    AND t.current_fence=s.execution_fence
    AND t.lease_token_hash=s.execution_lease_hash
    AND t.lease_expires_at=s.execution_lease_expires_at AND t.lease_expires_at>p_now
    AND a.command_id=s.command_id AND a.status='running' AND a.fence=s.execution_fence
    AND a.lease_token_hash=s.execution_lease_hash
    AND a.lease_expires_at=s.execution_lease_expires_at AND a.lease_expires_at>p_now
  FOR SHARE OF t,a;

  IF NOT FOUND THEN RETURN 'execution_invalid'; END IF;
  RETURN 'valid';
END
$$;

REVOKE ALL ON FUNCTION agent.runtime_lock_execution_right(uuid,uuid,uuid,bigint,timestamptz) FROM PUBLIC;
