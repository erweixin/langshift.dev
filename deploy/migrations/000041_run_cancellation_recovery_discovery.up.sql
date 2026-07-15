DROP FUNCTION agent.list_run_cancellation_tenants(uuid,uuid,integer,integer,integer,timestamptz);

CREATE FUNCTION agent.list_run_cancellation_tenants(
  p_store_epoch uuid,
  p_after uuid,
  p_limit integer,
  p_shard_index integer,
  p_shard_count integer,
  p_now timestamptz
) RETURNS TABLE(tenant_id text)
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,agent SET row_security=off AS $$
BEGIN
  IF p_store_epoch IS NULL OR p_limit IS NULL OR p_limit<1 OR p_limit>5000
    OR p_shard_count IS NULL OR p_shard_count<1 OR p_shard_index IS NULL
    OR p_shard_index<0 OR p_shard_index>=p_shard_count OR p_now IS NULL
  THEN
    RAISE EXCEPTION 'invalid run cancellation tenant scan arguments' USING ERRCODE='22023';
  END IF;

  RETURN QUERY
    SELECT c.tenant_id::text
    FROM agent.run_cancellations c
    JOIN agent.events e ON e.tenant_id=c.tenant_id
      AND e.aggregate_kind='run_cancellation' AND e.aggregate_id=c.id
      AND e.event_type='RunCancellationRequested' AND e.aggregate_version=1
    WHERE c.store_epoch=p_store_epoch AND e.store_epoch=p_store_epoch
      AND c.status IN ('requested','terminating') AND c.reconciliation_due_at<=p_now
      AND (p_after IS NULL OR c.tenant_id>p_after)
      AND ((hashtextextended(c.tenant_id::text,0) & 9223372036854775807) % p_shard_count)=p_shard_index
    GROUP BY c.tenant_id
    ORDER BY c.tenant_id
    LIMIT p_limit;
END
$$;

REVOKE ALL ON FUNCTION agent.list_run_cancellation_tenants(uuid,uuid,integer,integer,integer,timestamptz) FROM PUBLIC;
