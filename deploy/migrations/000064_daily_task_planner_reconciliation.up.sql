ALTER TABLE product.daily_task_generations
  DROP CONSTRAINT daily_task_generations_lifecycle_contract;

ALTER TABLE product.daily_task_generations
  ADD CONSTRAINT daily_task_generations_lifecycle_contract CHECK (
    (status='generating' AND planner_run_id IS NOT NULL AND task_id IS NULL AND failure_reason IS NULL AND completed_at IS NULL)
    OR (status='succeeded' AND planner_run_id IS NOT NULL AND task_id IS NOT NULL AND failure_reason IS NULL AND completed_at IS NOT NULL)
    OR (status='failed' AND planner_run_id IS NOT NULL AND task_id IS NULL AND char_length(failure_reason) BETWEEN 1 AND 128 AND completed_at IS NOT NULL)
    OR (status='superseded' AND task_id IS NULL AND failure_reason='focus_or_route_changed' AND completed_at IS NOT NULL)
  );

CREATE UNIQUE INDEX daily_task_generations_one_live_daily
  ON product.daily_task_generations(tenant_id,user_id,scheduled_for)
  WHERE status IN ('generating','succeeded');

CREATE FUNCTION agent.list_daily_task_planner_reconciliation_tenants(
  p_after_tenant uuid,
  p_limit integer
) RETURNS TABLE(tenant_id uuid)
LANGUAGE sql
SECURITY DEFINER
SET search_path = pg_catalog, agent, product
SET row_security = off
AS $$
  SELECT DISTINCT g.tenant_id
  FROM product.daily_task_generations g
  JOIN agent.runs r ON r.tenant_id=g.tenant_id AND r.id=g.planner_run_id
  WHERE g.status='generating'
    AND g.planner_run_id IS NOT NULL
    AND r.status IN ('succeeded','failed','cancelled','expired')
    AND (p_after_tenant IS NULL OR g.tenant_id>p_after_tenant)
  ORDER BY g.tenant_id
  LIMIT LEAST(GREATEST(p_limit,1),5000)
$$;

REVOKE ALL ON FUNCTION agent.list_daily_task_planner_reconciliation_tenants(uuid,integer) FROM PUBLIC;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service') THEN
    GRANT SELECT ON product.user_preferences TO lites_product_service;
    GRANT EXECUTE ON FUNCTION agent.list_daily_task_planner_reconciliation_tenants(uuid,integer) TO lites_product_service;
  END IF;
END
$$;
