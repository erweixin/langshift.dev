BEGIN;
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_contract_service') THEN
    REVOKE EXECUTE ON FUNCTION contracts.apply_approved_entitlement_proposal(uuid,uuid,uuid,uuid,uuid,timestamptz) FROM lites_contract_service;
    REVOKE SELECT,INSERT ON contracts.entitlement_proposals,contracts.entitlement_approval_decisions FROM lites_contract_service;
    REVOKE SELECT ON contracts.contract_entitlements FROM lites_contract_service;
  END IF;
END
$$;
DROP FUNCTION IF EXISTS contracts.apply_approved_entitlement_proposal(uuid,uuid,uuid,uuid,uuid,timestamptz);
DROP TRIGGER IF EXISTS contract_entitlements_append ON contracts.contract_entitlements;
DROP FUNCTION IF EXISTS contracts.enforce_entitlement_append();
DROP TRIGGER IF EXISTS entitlement_approvals_reject ON contracts.entitlement_approval_decisions;
DROP FUNCTION IF EXISTS contracts.finalize_entitlement_rejection();
DROP TRIGGER IF EXISTS entitlement_approvals_validate ON contracts.entitlement_approval_decisions;
DROP FUNCTION IF EXISTS contracts.validate_entitlement_approval();
DROP TRIGGER IF EXISTS entitlement_proposals_validate ON contracts.entitlement_proposals;
DROP FUNCTION IF EXISTS contracts.validate_entitlement_proposal();
DROP TRIGGER IF EXISTS entitlement_proposals_lifecycle ON contracts.entitlement_proposals;
DROP FUNCTION IF EXISTS contracts.enforce_entitlement_proposal_lifecycle();
DROP TABLE contracts.entitlement_approval_decisions;
DROP TABLE contracts.entitlement_proposals;
DROP INDEX IF EXISTS contracts.contract_entitlements_current_idx;
ALTER TABLE contracts.contract_entitlements
  DROP CONSTRAINT IF EXISTS contract_entitlements_scope,
  DROP CONSTRAINT IF EXISTS contract_entitlements_contract_exact_fk,
  DROP CONSTRAINT IF EXISTS contract_entitlements_tenant_id_id_unique;
COMMIT;
