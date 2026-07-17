DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM public.lites_schema_migrations
    WHERE version=64 AND name='daily_task_planner_reconciliation'
  ) THEN
    RAISE EXCEPTION 'daily task planner reconciliation migration is not installed';
  END IF;

  IF NOT EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname='product' AND indexname='daily_task_generations_one_live_daily')
    OR to_regprocedure('agent.list_daily_task_planner_reconciliation_tenants(uuid,integer)') IS NULL THEN
    RAISE EXCEPTION 'daily task planner reconciliation contract is incomplete';
  END IF;

  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service')
    AND (NOT has_function_privilege('lites_product_service','agent.list_daily_task_planner_reconciliation_tenants(uuid,integer)','EXECUTE')
      OR NOT has_table_privilege('lites_product_service','product.user_preferences','SELECT')) THEN
    RAISE EXCEPTION 'product service cannot discover daily task planner reconciliation tenants';
  END IF;
END
$$;

SELECT json_build_object(
  'status','passed',
  'schema_version',64,
  'daily_task_planner','terminal_reconciliation_enabled'
) AS current_schema_verification;
