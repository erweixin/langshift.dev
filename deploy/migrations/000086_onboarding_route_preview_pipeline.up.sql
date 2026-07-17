BEGIN;

ALTER TABLE identity.onboarding_sessions
  ADD COLUMN mission_id uuid,
  ADD CONSTRAINT onboarding_sessions_mission_fk FOREIGN KEY (mission_id) REFERENCES product.missions(id) ON DELETE RESTRICT;

CREATE INDEX onboarding_sessions_route_reconciliation_idx
  ON identity.onboarding_sessions(tenant_id,status,updated_at,id)
  WHERE status='route_generating';

CREATE FUNCTION identity.list_onboarding_route_reconciliation_tenants(p_after uuid,p_limit integer)
RETURNS TABLE(tenant_id uuid)
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,identity,product SET row_security=off AS $$
BEGIN
  IF p_limit IS NULL OR p_limit<1 OR p_limit>5000 THEN
    RAISE EXCEPTION 'invalid onboarding route reconciliation scan' USING ERRCODE='22023';
  END IF;
  RETURN QUERY
    SELECT s.tenant_id
    FROM identity.onboarding_sessions s
    JOIN product.route_revisions r ON r.tenant_id=s.tenant_id AND r.id=s.route_revision_id
    WHERE s.status='route_generating' AND r.status IN ('proposed','failed','stale')
      AND (p_after IS NULL OR s.tenant_id>p_after)
    GROUP BY s.tenant_id ORDER BY s.tenant_id LIMIT p_limit;
END
$$;
REVOKE ALL ON FUNCTION identity.list_onboarding_route_reconciliation_tenants(uuid,integer) FROM PUBLIC;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service') THEN
    GRANT USAGE ON SCHEMA identity TO lites_product_service;
    GRANT SELECT,UPDATE ON identity.onboarding_sessions TO lites_product_service;
    GRANT SELECT,INSERT ON identity.onboarding_claims TO lites_product_service;
    GRANT EXECUTE ON FUNCTION identity.list_onboarding_route_reconciliation_tenants(uuid,integer) TO lites_product_service;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_identity_service') THEN
    GRANT SELECT ON product.role_profiles TO lites_identity_service;
    GRANT SELECT,INSERT,UPDATE ON product.missions TO lites_identity_service;
  END IF;
END
$$;

COMMIT;
