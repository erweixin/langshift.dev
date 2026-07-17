DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service') THEN
    REVOKE EXECUTE ON FUNCTION agent.lock_owned_portfolio_export(uuid,uuid,uuid,bigint,text) FROM lites_product_service;
  END IF;
END
$$;

DROP FUNCTION agent.lock_owned_portfolio_export(uuid,uuid,uuid,bigint,text);
