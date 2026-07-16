DROP INDEX IF EXISTS agent.tool_calls_expired_effect_idx;

CREATE INDEX tool_calls_expired_effect_idx
  ON agent.tool_calls(tenant_id,lease_expires_at,id)
  WHERE status='executing' AND effect_class='reconcilable_write';

CREATE OR REPLACE FUNCTION agent.list_expired_tool_effect_tenants(
  p_store_epoch uuid,
  p_after uuid,
  p_limit integer,
  p_shard_index integer,
  p_shard_count integer,
  p_now timestamptz
) RETURNS TABLE(tenant_id text)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, agent SET row_security = off AS $$
BEGIN
  IF p_store_epoch IS NULL OR p_limit IS NULL OR p_limit < 1 OR p_limit > 5000
    OR p_shard_count IS NULL OR p_shard_count < 1 OR p_shard_index IS NULL OR p_shard_index < 0 OR p_shard_index >= p_shard_count
    OR p_now IS NULL THEN
    RAISE EXCEPTION 'invalid expired effect tenant scan arguments' USING ERRCODE = '22023';
  END IF;
  RETURN QUERY
    SELECT t.tenant_id::text
    FROM agent.tool_calls t
    JOIN agent.tool_effects e ON e.tenant_id=t.tenant_id AND e.tool_call_id=t.id
    JOIN agent.outbox o ON o.tenant_id=t.tenant_id AND o.command_id=t.active_command_id
    JOIN agent.inbox i ON i.tenant_id=t.tenant_id AND i.command_id=t.active_command_id AND i.owner_attempt_id=t.active_attempt_id
    JOIN agent.jobs j ON j.tenant_id=t.tenant_id AND j.command_id=t.active_command_id
    JOIN agent.job_attempts a ON a.tenant_id=t.tenant_id AND a.id=t.active_attempt_id AND a.job_id=j.id AND a.command_id=j.command_id
    WHERE o.store_epoch=p_store_epoch
      AND t.status='executing' AND t.effect_class='reconcilable_write'
      AND t.lease_expires_at<=p_now
      AND e.status='executing' AND e.execution_attempt_id=t.active_attempt_id AND e.execution_fence=t.current_fence
      AND i.status='running' AND i.fence=t.current_fence AND i.lease_expires_at=t.lease_expires_at AND i.lease_expires_at<=p_now
      AND j.status='running'
      AND a.status='running' AND a.fence=t.current_fence AND a.lease_expires_at=t.lease_expires_at AND a.lease_expires_at<=p_now
      AND (p_after IS NULL OR t.tenant_id>p_after)
      AND ((hashtextextended(t.tenant_id::text,0) & 9223372036854775807) % p_shard_count)=p_shard_index
    GROUP BY t.tenant_id
    ORDER BY t.tenant_id
    LIMIT p_limit;
END
$$;

REVOKE ALL ON FUNCTION agent.list_expired_tool_effect_tenants(uuid,uuid,integer,integer,integer,timestamptz) FROM PUBLIC;
