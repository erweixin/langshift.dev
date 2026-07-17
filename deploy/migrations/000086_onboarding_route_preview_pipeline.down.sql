BEGIN;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service') THEN
    REVOKE EXECUTE ON FUNCTION identity.list_onboarding_route_reconciliation_tenants(uuid,integer) FROM lites_product_service;
    REVOKE SELECT,UPDATE ON identity.onboarding_sessions FROM lites_product_service;
    REVOKE SELECT,INSERT ON identity.onboarding_claims FROM lites_product_service;
  END IF;
END
$$;

DROP FUNCTION identity.list_onboarding_route_reconciliation_tenants(uuid,integer);
DROP INDEX identity.onboarding_sessions_route_reconciliation_idx;
ALTER TABLE identity.onboarding_sessions DROP CONSTRAINT onboarding_sessions_mission_fk, DROP COLUMN mission_id;

COMMIT;
