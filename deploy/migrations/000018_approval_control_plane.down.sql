DROP FUNCTION IF EXISTS agent.list_expired_approval_tenants(uuid,uuid,integer,integer,integer,timestamptz);
DROP INDEX IF EXISTS agent.approvals_pending_idx;
DROP TRIGGER IF EXISTS approvals_lifecycle ON agent.approvals;
DROP FUNCTION IF EXISTS agent.enforce_approval_lifecycle();

ALTER TABLE agent.approval_decisions
  DROP CONSTRAINT IF EXISTS approval_decisions_event_unique,
  DROP CONSTRAINT IF EXISTS approval_decisions_event_fk,
  DROP CONSTRAINT IF EXISTS approval_decisions_tenant_approval_fk,
  DROP CONSTRAINT IF EXISTS approval_decisions_session_fk,
  DROP CONSTRAINT IF EXISTS approval_decisions_actor_fk,
  DROP CONSTRAINT IF EXISTS approval_decisions_scope_contract,
  DROP CONSTRAINT IF EXISTS approval_decisions_mode_contract,
  DROP CONSTRAINT IF EXISTS approval_decisions_decision_contract,
  DROP COLUMN IF EXISTS decision_mode,
  DROP COLUMN IF EXISTS decision_event_id,
  ADD CONSTRAINT approval_decisions_approval_id_fk FOREIGN KEY (approval_id) REFERENCES agent.approvals(id) ON DELETE CASCADE;

ALTER TABLE agent.approvals
  DROP CONSTRAINT IF EXISTS approvals_requested_by_fk,
  DROP CONSTRAINT IF EXISTS approvals_lifecycle_contract,
  DROP CONSTRAINT IF EXISTS approvals_scope_contract,
  DROP CONSTRAINT IF EXISTS approvals_tenant_id_id_unique,
  DROP COLUMN IF EXISTS expired_at,
  DROP COLUMN IF EXISTS invalidated_at,
  DROP COLUMN IF EXISTS rejected_at,
  DROP COLUMN IF EXISTS granted_at;
