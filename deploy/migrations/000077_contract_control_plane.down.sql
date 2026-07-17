DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_contract_service') THEN
    REVOKE EXECUTE ON FUNCTION contracts.apply_approved_contract_proposal(uuid,uuid,uuid,uuid,timestamptz) FROM lites_contract_service;
    REVOKE SELECT,INSERT ON contracts.contract_proposals,contracts.contract_approval_decisions FROM lites_contract_service;
  END IF;
END
$$;

ALTER TABLE contracts.contract_audit_events
  DROP CONSTRAINT IF EXISTS contract_audit_events_completeness,
  DROP CONSTRAINT IF EXISTS contract_audit_events_contract_exact_fk;
DROP FUNCTION IF EXISTS contracts.apply_approved_contract_proposal(uuid,uuid,uuid,uuid,timestamptz);
DROP TRIGGER IF EXISTS memberships_contract_seat ON identity.memberships;
DROP FUNCTION IF EXISTS contracts.sync_membership_seat();
DROP INDEX IF EXISTS contracts.seat_allocations_contract_status_idx;
DROP INDEX IF EXISTS contracts.seat_allocations_one_active_membership_idx;
DROP TRIGGER IF EXISTS seat_allocations_lifecycle ON contracts.seat_allocations;
DROP FUNCTION IF EXISTS contracts.enforce_seat_allocation_lifecycle();
ALTER TABLE contracts.seat_allocations
  DROP CONSTRAINT IF EXISTS seat_allocations_lifecycle_contract,
  DROP CONSTRAINT IF EXISTS seat_allocations_membership_exact_fk,
  DROP CONSTRAINT IF EXISTS seat_allocations_contract_exact_fk,
  DROP CONSTRAINT IF EXISTS seat_allocations_tenant_id_id_unique;
DROP TRIGGER IF EXISTS contracts_lifecycle ON contracts.contracts;
DROP FUNCTION IF EXISTS contracts.enforce_contract_lifecycle();
DROP TRIGGER IF EXISTS contract_approval_decisions_validate ON contracts.contract_approval_decisions;
DROP FUNCTION IF EXISTS contracts.validate_contract_approval_decision();
DROP TRIGGER IF EXISTS contract_approval_decisions_reject ON contracts.contract_approval_decisions;
DROP FUNCTION IF EXISTS contracts.finalize_contract_rejection();
DROP TRIGGER IF EXISTS contract_proposals_validate ON contracts.contract_proposals;
DROP FUNCTION IF EXISTS contracts.validate_contract_proposal();
DROP TRIGGER IF EXISTS contract_proposals_lifecycle ON contracts.contract_proposals;
DROP FUNCTION IF EXISTS contracts.enforce_contract_proposal_lifecycle();
DROP INDEX IF EXISTS contracts.contract_approval_decisions_proposal_idx;
DROP INDEX IF EXISTS contracts.contract_proposals_status_expiry_idx;
DROP TABLE IF EXISTS contracts.contract_approval_decisions;
DROP TABLE IF EXISTS contracts.contract_proposals;
DROP INDEX IF EXISTS contracts.contracts_one_live_per_tenant_idx;
ALTER TABLE contracts.contracts
  DROP CONSTRAINT IF EXISTS contracts_control_plane_contract,
  DROP CONSTRAINT IF EXISTS contracts_tenant_id_id_unique;
ALTER TABLE identity.memberships DROP CONSTRAINT IF EXISTS memberships_tenant_id_id_unique;
