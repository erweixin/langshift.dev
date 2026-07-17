DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service') THEN
    REVOKE EXECUTE ON FUNCTION agent.list_portfolio_export_finalization_tenants(uuid,uuid,integer) FROM lites_product_service;
  END IF;
END
$$;

DROP FUNCTION IF EXISTS agent.list_portfolio_export_finalization_tenants(uuid,uuid,integer);
