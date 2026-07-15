DROP FUNCTION IF EXISTS agent.list_run_cancellation_tenants(uuid,uuid,integer,integer,integer,timestamptz);
DROP TRIGGER IF EXISTS run_cancellation_event_guard ON agent.run_cancellations;
DROP FUNCTION IF EXISTS agent.validate_run_cancellation_events();
DROP TRIGGER IF EXISTS run_cancellation_lifecycle ON agent.run_cancellations;
DROP FUNCTION IF EXISTS agent.enforce_run_cancellation_lifecycle();
DROP INDEX IF EXISTS agent.run_cancellations_reconcile_idx;

ALTER TABLE agent.runs
  DROP CONSTRAINT IF EXISTS runs_active_cancellation_fk;

ALTER TABLE agent.run_cancellations
  DROP CONSTRAINT IF EXISTS run_cancellations_scope_contract,
  DROP CONSTRAINT IF EXISTS run_cancellations_settlement_event_fk,
  DROP CONSTRAINT IF EXISTS run_cancellations_request_event_fk,
  DROP CONSTRAINT IF EXISTS run_cancellations_run_fk,
  DROP CONSTRAINT IF EXISTS run_cancellations_root_unique,
  DROP CONSTRAINT IF EXISTS run_cancellations_generation_unique,
  DROP CONSTRAINT IF EXISTS run_cancellations_tenant_id_id_unique,
  DROP COLUMN IF EXISTS reconciliation_due_at,
  DROP COLUMN IF EXISTS settlement_payload_hash,
  DROP COLUMN IF EXISTS settlement_payload_ref,
  DROP COLUMN IF EXISTS request_payload_hash,
  DROP COLUMN IF EXISTS request_payload_ref,
  DROP COLUMN IF EXISTS settlement_event_id,
  DROP COLUMN IF EXISTS request_event_id,
  DROP COLUMN IF EXISTS request_hash,
  DROP COLUMN IF EXISTS store_epoch,
  ADD CONSTRAINT run_cancellations_run_id_fk FOREIGN KEY (run_id)
    REFERENCES agent.runs(id) ON DELETE CASCADE;

ALTER TABLE agent.runs
  DROP CONSTRAINT IF EXISTS runs_cancellation_contract,
  DROP COLUMN IF EXISTS active_cancellation_id,
  DROP COLUMN IF EXISTS cancel_generation;
