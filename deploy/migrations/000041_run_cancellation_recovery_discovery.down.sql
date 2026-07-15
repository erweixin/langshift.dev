DROP FUNCTION agent.list_run_cancellation_tenants(uuid,uuid,integer,integer,integer,timestamptz);

CREATE FUNCTION agent.list_run_cancellation_tenants(
  p_anchor_tenant uuid,
  p_after_tenant uuid,
  p_shard_count integer,
  p_shard_index integer,
  p_limit integer,
  p_now timestamptz
) RETURNS TABLE(tenant_id uuid)
LANGUAGE sql STABLE SECURITY INVOKER SET search_path=pg_catalog,agent AS $$
  SELECT c.tenant_id
  FROM agent.run_cancellations c
  WHERE c.status IN ('requested','terminating') AND c.reconciliation_due_at<=p_now
    AND c.tenant_id<>p_anchor_tenant AND c.tenant_id>p_after_tenant
    AND mod(abs(hashtextextended(c.tenant_id::text,0)),p_shard_count)=p_shard_index
  GROUP BY c.tenant_id
  ORDER BY c.tenant_id
  LIMIT p_limit
$$;

REVOKE ALL ON FUNCTION agent.list_run_cancellation_tenants(uuid,uuid,integer,integer,integer,timestamptz) FROM PUBLIC;
