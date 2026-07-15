ALTER TABLE agent.idempotency_responses
  ADD COLUMN prepared_event_payload_ref text,
  ADD COLUMN prepared_event_payload_hash text,
  ADD COLUMN prepared_event_payload_at timestamptz,
  ADD CONSTRAINT idempotency_prepared_event_payload_contract CHECK (
    (prepared_event_payload_ref IS NULL AND prepared_event_payload_hash IS NULL AND prepared_event_payload_at IS NULL)
    OR
    (NULLIF(prepared_event_payload_ref,'') IS NOT NULL
      AND prepared_event_payload_hash ~ '^[0-9a-f]{64}$'
      AND prepared_event_payload_at IS NOT NULL)
  );

COMMENT ON COLUMN agent.idempotency_responses.prepared_event_payload_ref IS
  'Durably selected immutable encrypted event payload; retries must reuse this exact pointer.';
