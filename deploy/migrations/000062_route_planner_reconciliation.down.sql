DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service') THEN
    REVOKE EXECUTE ON FUNCTION agent.list_route_planner_reconciliation_tenants(uuid,integer) FROM lites_product_service;
  END IF;
END
$$;

DROP FUNCTION IF EXISTS agent.list_route_planner_reconciliation_tenants(uuid,integer);

ALTER TABLE product.route_revisions
  DROP CONSTRAINT IF EXISTS route_revisions_failure_contract,
  DROP COLUMN IF EXISTS failure_reason;
