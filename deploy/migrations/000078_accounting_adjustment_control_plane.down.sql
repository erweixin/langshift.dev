DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_contract_service') THEN
    REVOKE EXECUTE ON FUNCTION contracts.apply_approved_accounting_adjustment(uuid,uuid,uuid,uuid,timestamptz) FROM lites_contract_service;
    REVOKE SELECT,INSERT ON contracts.accounting_adjustment_proposals,contracts.accounting_adjustment_approval_decisions FROM lites_contract_service;
    REVOKE SELECT ON contracts.credit_buckets,contracts.manual_adjustments FROM lites_contract_service;
    REVOKE SELECT,INSERT,UPDATE ON agent.event_cursors FROM lites_contract_service;
  END IF;
END
$$;

DROP FUNCTION IF EXISTS contracts.apply_approved_accounting_adjustment(uuid,uuid,uuid,uuid,timestamptz);
ALTER TABLE contracts.manual_adjustments
  DROP CONSTRAINT IF EXISTS manual_adjustments_completeness,
  DROP CONSTRAINT IF EXISTS manual_adjustments_bucket_exact_fk,
  DROP COLUMN IF EXISTS approval_event_ids,
  DROP COLUMN IF EXISTS after_granted_units,
  DROP COLUMN IF EXISTS before_granted_units,
  DROP COLUMN IF EXISTS after_bucket_version,
  DROP COLUMN IF EXISTS before_bucket_version,
  DROP COLUMN IF EXISTS reason_hash,
  DROP COLUMN IF EXISTS reason_ref;

CREATE OR REPLACE FUNCTION contracts.enforce_credit_bucket_accounting() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE reserved_delta bigint; settled_delta bigint;
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'credit bucket deletion is forbidden'; END IF;
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.contract_id IS DISTINCT FROM OLD.contract_id OR NEW.bucket_kind IS DISTINCT FROM OLD.bucket_kind
    OR NEW.granted_units IS DISTINCT FROM OLD.granted_units OR NEW.starts_at IS DISTINCT FROM OLD.starts_at
    OR NEW.expires_at IS DISTINCT FROM OLD.expires_at OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'immutable credit bucket scope mutation is forbidden';
  END IF;
  IF NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at THEN
    RAISE EXCEPTION 'credit bucket version must advance exactly once';
  END IF;
  reserved_delta:=NEW.reserved_units-OLD.reserved_units;
  settled_delta:=NEW.settled_units-OLD.settled_units;
  IF reserved_delta>0 AND settled_delta=0 THEN RETURN NEW; END IF;
  IF reserved_delta<0 AND settled_delta=0 THEN RETURN NEW; END IF;
  IF reserved_delta<0 AND settled_delta>=0 AND settled_delta<=-reserved_delta THEN RETURN NEW; END IF;
  RAISE EXCEPTION 'invalid credit bucket accounting transition';
END
$$;

DROP INDEX IF EXISTS contracts.accounting_adjustment_approvals_proposal_idx;
DROP INDEX IF EXISTS contracts.accounting_adjustment_proposals_status_idx;
DROP TRIGGER IF EXISTS accounting_adjustment_approvals_reject ON contracts.accounting_adjustment_approval_decisions;
DROP FUNCTION IF EXISTS contracts.finalize_accounting_adjustment_rejection();
DROP TRIGGER IF EXISTS accounting_adjustment_approvals_validate ON contracts.accounting_adjustment_approval_decisions;
DROP FUNCTION IF EXISTS contracts.validate_accounting_adjustment_approval();
DROP TRIGGER IF EXISTS accounting_adjustment_proposals_validate ON contracts.accounting_adjustment_proposals;
DROP FUNCTION IF EXISTS contracts.validate_accounting_adjustment_proposal();
DROP TRIGGER IF EXISTS accounting_adjustment_proposals_lifecycle ON contracts.accounting_adjustment_proposals;
DROP FUNCTION IF EXISTS contracts.enforce_accounting_adjustment_proposal_lifecycle();
DROP TABLE IF EXISTS contracts.accounting_adjustment_approval_decisions;
DROP TABLE IF EXISTS contracts.accounting_adjustment_proposals;
