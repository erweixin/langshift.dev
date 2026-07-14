CREATE OR REPLACE FUNCTION agent.list_recoverable_repair_tenants(
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
    RAISE EXCEPTION 'invalid repair recovery tenant scan arguments' USING ERRCODE = '22023';
  END IF;
  RETURN QUERY
    SELECT r.tenant_id::text
    FROM agent.repair_commands r
    JOIN agent.events e ON e.tenant_id=r.tenant_id
      AND e.aggregate_kind='repair_command' AND e.aggregate_id=r.id
      AND e.event_type='RepairCommandApproved' AND e.aggregate_version=r.version
    WHERE e.store_epoch=p_store_epoch
      AND r.repair_kind='tool_effect_resolution' AND r.status='approved' AND r.expires_at>p_now
      AND (p_after IS NULL OR r.tenant_id>p_after)
      AND ((hashtextextended(r.tenant_id::text,0) & 9223372036854775807) % p_shard_count)=p_shard_index
    GROUP BY r.tenant_id
    ORDER BY r.tenant_id
    LIMIT p_limit;
END
$$;

REVOKE ALL ON FUNCTION agent.list_recoverable_repair_tenants(uuid,uuid,integer,integer,integer,timestamptz) FROM PUBLIC;
