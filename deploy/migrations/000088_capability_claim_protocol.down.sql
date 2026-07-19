BEGIN;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service') THEN
    REVOKE INSERT ON product.capabilities FROM lites_product_service;
    REVOKE SELECT,INSERT ON product.capability_claims,product.claim_evidence_links FROM lites_product_service;
  END IF;
END
$$;

COMMIT;
