BEGIN;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service') THEN
    REVOKE SELECT ON contracts.usage_ledger FROM lites_product_service;
    REVOKE USAGE ON SCHEMA contracts FROM lites_product_service;
    REVOKE INSERT ON product.aggregate_metrics,product.aggregate_snapshots FROM lites_product_service;
  END IF;
END
$$;

COMMIT;
