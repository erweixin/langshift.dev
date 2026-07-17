BEGIN;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service') THEN
    REVOKE INSERT ON product.role_profiles FROM lites_product_service;
  END IF;
END
$$;

COMMIT;
