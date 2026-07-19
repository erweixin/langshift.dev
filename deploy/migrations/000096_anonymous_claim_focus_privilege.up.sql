BEGIN;

-- The identity import worker owns the anonymous Claim saga. Its destination
-- transaction creates the single focused Mission together with the accepted
-- Route and GenerateDailyTask command, so focus mutation is part of that
-- atomic effect rather than a separate product-service call.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_identity_service') THEN
    GRANT SELECT,INSERT,UPDATE ON product.mission_focuses TO lites_identity_service;
  END IF;
END
$$;

COMMIT;
