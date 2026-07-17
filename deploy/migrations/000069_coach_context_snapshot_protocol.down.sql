DO $$ BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_agent_control_service') THEN
    REVOKE EXECUTE ON FUNCTION agent.read_coach_context_snapshot(uuid,uuid,uuid), agent.lock_coach_context_fence(uuid,uuid,uuid,uuid,bigint,bigint,bigint,text,uuid,bigint,uuid,bigint,bigint) FROM lites_agent_control_service;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_agent_service') THEN
    REVOKE EXECUTE ON FUNCTION agent.read_coach_context_snapshot(uuid,uuid,uuid), agent.lock_coach_context_fence(uuid,uuid,uuid,uuid,bigint,bigint,bigint,text,uuid,bigint,uuid,bigint,bigint) FROM lites_agent_service;
  END IF;
END $$;
DROP FUNCTION IF EXISTS agent.lock_coach_context_fence(uuid,uuid,uuid,uuid,bigint,bigint,bigint,text,uuid,bigint,uuid,bigint,bigint);
DROP FUNCTION IF EXISTS agent.read_coach_context_snapshot(uuid,uuid,uuid);
DROP TABLE IF EXISTS agent.coach_context_snapshots;
ALTER TABLE agent.run_messages DROP CONSTRAINT run_messages_scope_contract;
ALTER TABLE agent.run_messages ADD CONSTRAINT run_messages_scope_contract CHECK (
  message_index>=0 AND role IN ('user','assistant','tool')
  AND NULLIF(payload_ref,'') IS NOT NULL
  AND payload_hash ~ '^[0-9a-f]{64}$' AND content_hash ~ '^[0-9a-f]{64}$'
  AND content_type='application/json'
  AND source_kind IN ('conversation_user','agent_output','tool_result')
  AND trust_label IN ('system_trusted','user_asserted','derived','untrusted_external')
  AND ((role='user' AND source_kind='conversation_user' AND trust_label IN ('user_asserted','untrusted_external'))
    OR (role='assistant' AND source_kind='agent_output' AND trust_label='derived')
    OR (role='tool' AND source_kind='tool_result' AND trust_label IN ('derived','untrusted_external')))
);
