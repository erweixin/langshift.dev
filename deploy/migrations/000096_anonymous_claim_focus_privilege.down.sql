BEGIN;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_identity_service') THEN
    REVOKE SELECT,INSERT,UPDATE ON product.mission_focuses FROM lites_identity_service;
  END IF;
END
$$;

COMMIT;
