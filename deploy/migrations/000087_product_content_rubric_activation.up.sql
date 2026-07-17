BEGIN;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service') THEN
    GRANT SELECT,INSERT ON product.rubric_versions TO lites_product_service;
  END IF;
END
$$;

COMMIT;
