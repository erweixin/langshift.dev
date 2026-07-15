DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM public.lites_schema_migrations
    WHERE version=39 AND name='idempotency_prepared_event_payload'
  ) OR (
    SELECT count(*) FROM information_schema.columns
    WHERE table_schema='agent' AND table_name='idempotency_responses'
      AND column_name IN ('prepared_event_payload_ref','prepared_event_payload_hash','prepared_event_payload_at')
  )<>3 OR NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid='agent.idempotency_responses'::regclass
      AND conname='idempotency_prepared_event_payload_contract'
  ) THEN
    RAISE EXCEPTION 'idempotency prepared event payload protocol is incomplete';
  END IF;
END
$$;

SELECT json_build_object('status','passed','schema_version',39,'idempotency_recovery','prepared_event_payload') AS current_schema_verification;
