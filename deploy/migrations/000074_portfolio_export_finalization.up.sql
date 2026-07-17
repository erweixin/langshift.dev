CREATE FUNCTION agent.list_portfolio_export_finalization_tenants(
  p_store_epoch uuid,
  p_after uuid,
  p_limit integer
) RETURNS TABLE(tenant_id text)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, product, agent SET row_security = off AS $$
BEGIN
  IF p_store_epoch IS NULL OR p_limit IS NULL OR p_limit<1 OR p_limit>5000 THEN
    RAISE EXCEPTION 'invalid portfolio export finalization tenant scan arguments' USING ERRCODE='22023';
  END IF;
  RETURN QUERY
    SELECT p.tenant_id::text
    FROM product.portfolio_exports p
    JOIN agent.runs r ON r.tenant_id=p.tenant_id AND r.id=p.run_id
    JOIN agent.events q ON q.tenant_id=p.tenant_id AND q.id=p.request_event_id AND q.store_epoch=p_store_epoch
    WHERE p.status IN ('requested','building')
      AND (
        r.status IN ('succeeded','failed','cancelled','expired')
        OR EXISTS (
          SELECT 1
          FROM agent.tool_calls t
          JOIN agent.tool_effects e ON e.tenant_id=t.tenant_id AND e.tool_call_id=t.id
          JOIN agent.events x ON x.tenant_id=t.tenant_id AND x.id=t.result_event_id AND x.store_epoch=p_store_epoch
          WHERE t.tenant_id=p.tenant_id AND t.run_id=p.run_id
            AND t.tool_name='artifact_export' AND t.status='succeeded'
            AND e.status='confirmed' AND NULLIF(e.external_resource_ref,'') IS NOT NULL
        )
      )
      AND (p_after IS NULL OR p.tenant_id>p_after)
    GROUP BY p.tenant_id ORDER BY p.tenant_id LIMIT p_limit;
END
$$;

REVOKE ALL ON FUNCTION agent.list_portfolio_export_finalization_tenants(uuid,uuid,integer) FROM PUBLIC;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service') THEN
    GRANT EXECUTE ON FUNCTION agent.list_portfolio_export_finalization_tenants(uuid,uuid,integer) TO lites_product_service;
    GRANT SELECT ON agent.runs,agent.tool_calls,agent.tool_effects,agent.job_attempts,agent.events,agent.outbox TO lites_product_service;
  END IF;
END
$$;
