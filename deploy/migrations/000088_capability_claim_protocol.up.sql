BEGIN;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service') THEN
    GRANT SELECT,INSERT ON product.capabilities TO lites_product_service;
    GRANT SELECT,INSERT ON product.capability_claims,product.claim_evidence_links TO lites_product_service;
    GRANT SELECT,UPDATE ON product.route_revisions,product.missions TO lites_product_service;
  END IF;
END
$$;

COMMIT;
