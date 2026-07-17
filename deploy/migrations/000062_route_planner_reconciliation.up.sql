ALTER TABLE product.route_revisions
  ADD COLUMN failure_reason text;

ALTER TABLE product.route_revisions
  ADD CONSTRAINT route_revisions_failure_contract
  CHECK (
    (status='failed' AND failure_reason IS NOT NULL AND char_length(failure_reason) BETWEEN 1 AND 128)
    OR
    (status<>'failed' AND failure_reason IS NULL)
  ) NOT VALID;

CREATE FUNCTION agent.list_route_planner_reconciliation_tenants(
  p_after_tenant uuid,
  p_limit integer
) RETURNS TABLE(tenant_id uuid)
LANGUAGE sql
SECURITY DEFINER
SET search_path = pg_catalog, agent, product
SET row_security = off
AS $$
  SELECT DISTINCT r.tenant_id
  FROM product.route_revisions r
  JOIN agent.runs ar ON ar.tenant_id=r.tenant_id AND ar.id=r.planner_run_id
  WHERE r.status='generating'
    AND r.planner_run_id IS NOT NULL
    AND ar.status IN ('succeeded','failed','cancelled','expired')
    AND (p_after_tenant IS NULL OR r.tenant_id>p_after_tenant)
  ORDER BY r.tenant_id
  LIMIT LEAST(GREATEST(p_limit,1),5000)
$$;

REVOKE ALL ON FUNCTION agent.list_route_planner_reconciliation_tenants(uuid,integer) FROM PUBLIC;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service') THEN
    GRANT EXECUTE ON FUNCTION agent.list_route_planner_reconciliation_tenants(uuid,integer) TO lites_product_service;
  END IF;
END
$$;
