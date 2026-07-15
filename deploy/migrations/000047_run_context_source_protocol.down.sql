DROP INDEX IF EXISTS agent.run_messages_context_idx;
DROP TRIGGER IF EXISTS run_messages_append_only ON agent.run_messages;

ALTER TABLE agent.run_messages
  DROP CONSTRAINT IF EXISTS run_messages_finalized_event_fk,
  DROP CONSTRAINT IF EXISTS run_messages_tenant_run_fk,
  DROP CONSTRAINT IF EXISTS run_messages_scope_contract,
  DROP CONSTRAINT IF EXISTS run_messages_tenant_id_id_unique,
  ALTER COLUMN finalized_at DROP NOT NULL,
  DROP COLUMN IF EXISTS finalized_event_id,
  DROP COLUMN IF EXISTS trust_label,
  DROP COLUMN IF EXISTS source_kind,
  DROP COLUMN IF EXISTS content_type,
  DROP COLUMN IF EXISTS payload_hash,
  ADD CONSTRAINT run_messages_run_id_fk FOREIGN KEY (run_id)
    REFERENCES agent.runs(id) ON DELETE CASCADE;
