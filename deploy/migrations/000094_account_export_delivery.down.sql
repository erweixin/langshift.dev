BEGIN;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_erasure_worker') THEN
    REVOKE UPDATE ON product.data_export_requests FROM lites_erasure_worker;
    REVOKE SELECT ON identity.tenants,product.mission_focuses,product.capability_claims,
      product.claim_evidence_links,product.artifacts,agent.conversations FROM lites_erasure_worker;
  END IF;
END
$$;

DROP INDEX IF EXISTS product.data_export_requests_owner_created_idx;
ALTER TABLE product.data_export_requests
  DROP CONSTRAINT IF EXISTS data_export_requests_lifecycle_contract,
  DROP CONSTRAINT IF EXISTS data_export_requests_scope_contract,
  DROP COLUMN IF EXISTS failure_code;

COMMIT;
