DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM agent.run_messages) THEN
    RAISE EXCEPTION 'run message context-source backfill required before migration 47' USING ERRCODE='55000';
  END IF;
END
$$;

ALTER TABLE agent.run_messages
  ADD COLUMN payload_hash text NOT NULL,
  ADD COLUMN content_type text NOT NULL,
  ADD COLUMN source_kind text NOT NULL,
  ADD COLUMN trust_label text NOT NULL,
  ADD COLUMN finalized_event_id uuid NOT NULL,
  ALTER COLUMN finalized_at SET NOT NULL,
  DROP CONSTRAINT run_messages_run_id_fk,
  ADD CONSTRAINT run_messages_tenant_id_id_unique UNIQUE (tenant_id,id),
  ADD CONSTRAINT run_messages_scope_contract CHECK (
    message_index>=0 AND role IN ('user','assistant','tool')
    AND NULLIF(payload_ref,'') IS NOT NULL
    AND payload_hash ~ '^[0-9a-f]{64}$' AND content_hash ~ '^[0-9a-f]{64}$'
    AND content_type='application/json'
    AND source_kind IN ('conversation_user','agent_output','tool_result')
    AND trust_label IN ('system_trusted','user_asserted','derived','untrusted_external')
    AND ((role='user' AND source_kind='conversation_user' AND trust_label IN ('user_asserted','untrusted_external'))
      OR (role='assistant' AND source_kind='agent_output' AND trust_label='derived')
      OR (role='tool' AND source_kind='tool_result' AND trust_label IN ('derived','untrusted_external')))
  ),
  ADD CONSTRAINT run_messages_tenant_run_fk FOREIGN KEY (tenant_id,run_id)
    REFERENCES agent.runs(tenant_id,id) ON DELETE RESTRICT,
  ADD CONSTRAINT run_messages_finalized_event_fk FOREIGN KEY (finalized_event_id)
    REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;

CREATE TRIGGER run_messages_append_only BEFORE UPDATE OR DELETE ON agent.run_messages
  FOR EACH ROW EXECUTE FUNCTION agent.reject_append_only_mutation();

CREATE INDEX run_messages_context_idx
  ON agent.run_messages(tenant_id,run_id,message_index,id);
