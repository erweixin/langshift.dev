DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service') THEN
    REVOKE EXECUTE ON FUNCTION agent.lock_owned_project_evaluation(uuid,uuid,uuid,bigint,uuid,bigint,text) FROM lites_product_service;
    REVOKE EXECUTE ON FUNCTION agent.list_project_test_reconciliation_tenants(uuid,integer) FROM lites_product_service;
    REVOKE SELECT,INSERT,UPDATE ON product.project_test_generations FROM lites_product_service;
  END IF;
END
$$;

DROP FUNCTION agent.list_project_test_reconciliation_tenants(uuid,integer);
DROP FUNCTION agent.lock_owned_project_evaluation(uuid,uuid,uuid,bigint,uuid,bigint,text);
DROP TRIGGER project_test_generations_lifecycle ON product.project_test_generations;
DROP FUNCTION product.enforce_project_test_generation_lifecycle();
DROP TABLE product.project_test_generations;
