DO $$
DECLARE lifecycle_definition text;
DECLARE event_definition text;
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM public.lites_schema_migrations
    WHERE version=58 AND name='run_cancellation_settlement_materialization'
  ) THEN
    RAISE EXCEPTION 'run cancellation settlement materialization migration is not installed';
  END IF;

  lifecycle_definition := pg_get_functiondef('agent.enforce_run_cancellation_lifecycle()'::regprocedure);
  event_definition := pg_get_functiondef('agent.validate_run_cancellation_events()'::regprocedure);
  IF position('OLD.status=''terminating'' AND NEW.status=''settled'' AND OLD.parent_cancellation_id IS NULL' IN lifecycle_definition)=0
    OR position('NEW.settlement_payload_ref IS DISTINCT FROM OLD.settlement_payload_ref' IN lifecycle_definition)=0 THEN
    RAISE EXCEPTION 'root settlement payload replacement is not transition-scoped';
  END IF;
  IF position('e.payload_ref=NEW.settlement_payload_ref' IN event_definition)=0
    OR position('e.payload_hash=NEW.settlement_payload_hash' IN event_definition)=0
    OR position('e.aggregate_version=r.run_version' IN event_definition)=0 THEN
    RAISE EXCEPTION 'settlement row is not bound to the exact terminal event payload and version';
  END IF;
END
$$;

SELECT json_build_object(
  'status','passed',
  'schema_version',58,
  'run_cancellation_settlement','terminal_event_payload_bound'
) AS current_schema_verification;
