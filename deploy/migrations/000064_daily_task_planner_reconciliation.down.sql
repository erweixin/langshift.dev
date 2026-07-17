DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service') THEN
    REVOKE SELECT ON product.user_preferences FROM lites_product_service;
    REVOKE EXECUTE ON FUNCTION agent.list_daily_task_planner_reconciliation_tenants(uuid,integer) FROM lites_product_service;
  END IF;
END
$$;

DROP FUNCTION IF EXISTS agent.list_daily_task_planner_reconciliation_tenants(uuid,integer);
DROP INDEX IF EXISTS product.daily_task_generations_one_live_daily;

ALTER TABLE product.daily_task_generations
  DROP CONSTRAINT daily_task_generations_lifecycle_contract;

ALTER TABLE product.daily_task_generations
  ADD CONSTRAINT daily_task_generations_lifecycle_contract CHECK (
    (status='generating' AND planner_run_id IS NOT NULL AND task_id IS NULL AND failure_reason IS NULL AND completed_at IS NULL)
    OR (status='succeeded' AND planner_run_id IS NOT NULL AND task_id IS NOT NULL AND failure_reason IS NULL AND completed_at IS NOT NULL)
    OR (status='failed' AND planner_run_id IS NOT NULL AND task_id IS NULL AND char_length(failure_reason) BETWEEN 1 AND 128 AND completed_at IS NOT NULL)
    OR (status='superseded' AND task_id IS NULL AND planner_run_id IS NULL AND failure_reason='focus_or_route_changed' AND completed_at IS NOT NULL)
  );
