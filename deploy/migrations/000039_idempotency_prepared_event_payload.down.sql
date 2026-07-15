ALTER TABLE agent.idempotency_responses
  DROP CONSTRAINT IF EXISTS idempotency_prepared_event_payload_contract,
  DROP COLUMN IF EXISTS prepared_event_payload_at,
  DROP COLUMN IF EXISTS prepared_event_payload_hash,
  DROP COLUMN IF EXISTS prepared_event_payload_ref;
