BEGIN;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service') THEN
    GRANT SELECT ON identity.users,identity.memberships TO lites_product_service;
    GRANT SELECT ON product.daily_tasks,product.projects,product.enrollments,product.cohorts,product.role_packs TO lites_product_service;
    GRANT SELECT,INSERT ON product.aggregate_metrics,product.aggregate_snapshots TO lites_product_service;
    GRANT USAGE ON SCHEMA contracts TO lites_product_service;
    GRANT SELECT ON contracts.usage_ledger TO lites_product_service;
  END IF;
END
$$;

COMMIT;
