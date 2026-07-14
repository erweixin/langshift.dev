CREATE INDEX approvals_pending_scope_idx ON agent.approvals(tenant_id,approval_kind,run_id,tool_call_id,target_version,id)
  WHERE status='pending';

CREATE OR REPLACE FUNCTION agent.list_stale_approval_tenants(
  p_store_epoch uuid,
  p_after uuid,
  p_limit integer,
  p_shard_index integer,
  p_shard_count integer
) RETURNS TABLE(tenant_id text)
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,agent,identity SET row_security=off AS $$
BEGIN
  IF p_store_epoch IS NULL OR p_limit IS NULL OR p_limit<1 OR p_limit>5000
    OR p_shard_count IS NULL OR p_shard_count<1 OR p_shard_index IS NULL OR p_shard_index<0 OR p_shard_index>=p_shard_count
  THEN RAISE EXCEPTION 'invalid stale approval tenant scan arguments' USING ERRCODE='22023'; END IF;
  RETURN QUERY
    SELECT a.tenant_id::text
    FROM agent.approvals a
    JOIN agent.events e ON e.tenant_id=a.tenant_id AND e.aggregate_kind='approval'
      AND e.aggregate_id=a.id AND e.aggregate_version=1 AND e.event_type='ApprovalRequested'
    LEFT JOIN identity.memberships m ON m.tenant_id=a.tenant_id AND m.user_id=a.requested_by AND m.status='active'
    LEFT JOIN agent.tool_calls t ON a.approval_kind='tool_execution' AND t.tenant_id=a.tenant_id AND t.id=a.tool_call_id
    LEFT JOIN agent.runs r ON a.approval_kind='stage_checkpoint' AND r.tenant_id=a.tenant_id AND r.id=a.run_id
    WHERE e.store_epoch=p_store_epoch AND a.status='pending'
      AND (p_after IS NULL OR a.tenant_id>p_after)
      AND ((hashtextextended(a.tenant_id::text,0) & 9223372036854775807) % p_shard_count)=p_shard_index
      AND (
        m.id IS NULL OR a.permission_snapshot<>format('membership:%s:v%s:role:%s',m.id,m.version,m.role)
        OR (a.approval_kind='tool_execution' AND (t.id IS NULL OR t.status<>'awaiting_approval' OR t.tool_call_version<>a.target_version))
        OR (a.approval_kind='stage_checkpoint' AND (r.id IS NULL OR r.status<>'waiting_approval' OR r.run_version<>a.target_version))
      )
    GROUP BY a.tenant_id ORDER BY a.tenant_id LIMIT p_limit;
END
$$;

REVOKE ALL ON FUNCTION agent.list_stale_approval_tenants(uuid,uuid,integer,integer,integer) FROM PUBLIC;
