DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM public.lites_schema_migrations
    WHERE version=40 AND name='run_cancellation_barrier'
  ) OR (
    SELECT count(*) FROM information_schema.columns
    WHERE table_schema='agent' AND table_name='runs'
      AND column_name IN ('cancel_generation','active_cancellation_id')
  )<>2 OR (
    SELECT count(*) FROM information_schema.columns
    WHERE table_schema='agent' AND table_name='run_cancellations'
      AND column_name IN ('store_epoch','request_hash','request_event_id','settlement_event_id',
        'request_payload_ref','request_payload_hash','settlement_payload_ref','settlement_payload_hash','reconciliation_due_at')
  )<>9 OR NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE conrelid='agent.run_cancellations'::regclass
      AND conname='run_cancellations_scope_contract'
  ) OR NOT EXISTS (
    SELECT 1 FROM pg_proc WHERE oid='agent.list_run_cancellation_tenants(uuid,uuid,integer,integer,integer,timestamptz)'::regprocedure
  ) THEN
    RAISE EXCEPTION 'run cancellation barrier protocol is incomplete';
  END IF;
END
$$;

SELECT json_build_object('status','passed','schema_version',40,'cancellation','event_backed_barrier') AS current_schema_verification;
