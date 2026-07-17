DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM public.lites_schema_migrations
    WHERE version=62 AND name='route_planner_reconciliation'
  ) THEN
    RAISE EXCEPTION 'route planner reconciliation migration is not installed';
  END IF;

  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='route_revisions_failure_contract')
    OR to_regprocedure('agent.list_route_planner_reconciliation_tenants(uuid,integer)') IS NULL THEN
    RAISE EXCEPTION 'route planner reconciliation contract is incomplete';
  END IF;

  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service')
    AND NOT has_function_privilege('lites_product_service','agent.list_route_planner_reconciliation_tenants(uuid,integer)','EXECUTE') THEN
    RAISE EXCEPTION 'product service cannot discover route planner reconciliation tenants';
  END IF;
END
$$;

SELECT json_build_object(
  'status','passed',
  'schema_version',62,
  'route_planner','terminal_reconciliation_enabled'
) AS current_schema_verification;
