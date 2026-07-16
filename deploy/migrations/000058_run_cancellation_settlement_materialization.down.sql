DROP TRIGGER run_cancellation_event_guard ON agent.run_cancellations;
DROP FUNCTION agent.validate_run_cancellation_events();

CREATE FUNCTION agent.validate_run_cancellation_events() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM agent.events e
    WHERE e.id=NEW.request_event_id AND e.tenant_id=NEW.tenant_id
      AND e.store_epoch=NEW.store_epoch AND e.aggregate_kind='run_cancellation'
      AND e.aggregate_id=NEW.id AND e.aggregate_version=1
      AND e.event_type='RunCancellationRequested'
  ) THEN RAISE EXCEPTION 'run cancellation lacks matching request event'; END IF;
  IF NEW.status='settled' AND NOT EXISTS (
    SELECT 1 FROM agent.events e JOIN agent.runs r ON r.tenant_id=e.tenant_id AND r.id=NEW.run_id
    WHERE e.id=NEW.settlement_event_id AND e.tenant_id=NEW.tenant_id
      AND e.store_epoch=NEW.store_epoch AND e.aggregate_kind='run'
      AND e.aggregate_id=NEW.run_id
      AND ((r.parent_run_id IS NULL AND e.event_type='RunCancelled')
        OR (r.parent_run_id IS NOT NULL AND e.event_type='ChildRunCompleted'))
  ) THEN RAISE EXCEPTION 'settled run cancellation lacks matching terminal event'; END IF;
  RETURN NULL;
END
$$;

CREATE CONSTRAINT TRIGGER run_cancellation_event_guard
  AFTER INSERT OR UPDATE ON agent.run_cancellations
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION agent.validate_run_cancellation_events();

DROP TRIGGER run_cancellation_lifecycle ON agent.run_cancellations;
DROP FUNCTION agent.enforce_run_cancellation_lifecycle();

CREATE FUNCTION agent.enforce_run_cancellation_lifecycle() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'run cancellation deletion is forbidden'; END IF;
  IF OLD.status='settled' THEN RAISE EXCEPTION 'settled run cancellation mutation is forbidden'; END IF;
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.run_id IS DISTINCT FROM OLD.run_id OR NEW.root_cancellation_id IS DISTINCT FROM OLD.root_cancellation_id
    OR NEW.parent_cancellation_id IS DISTINCT FROM OLD.parent_cancellation_id
    OR NEW.cancel_generation IS DISTINCT FROM OLD.cancel_generation OR NEW.store_epoch IS DISTINCT FROM OLD.store_epoch
    OR NEW.requested_by IS DISTINCT FROM OLD.requested_by OR NEW.requested_at IS DISTINCT FROM OLD.requested_at
    OR NEW.reason IS DISTINCT FROM OLD.reason OR NEW.request_hash IS DISTINCT FROM OLD.request_hash
    OR NEW.request_event_id IS DISTINCT FROM OLD.request_event_id
    OR NEW.request_payload_ref IS DISTINCT FROM OLD.request_payload_ref
    OR NEW.request_payload_hash IS DISTINCT FROM OLD.request_payload_hash
    OR NEW.settlement_payload_ref IS DISTINCT FROM OLD.settlement_payload_ref
    OR NEW.settlement_payload_hash IS DISTINCT FROM OLD.settlement_payload_hash
    OR NEW.attempt_cancelled_payload_ref IS DISTINCT FROM OLD.attempt_cancelled_payload_ref
    OR NEW.attempt_cancelled_payload_hash IS DISTINCT FROM OLD.attempt_cancelled_payload_hash
    OR NEW.propagation_command_id IS DISTINCT FROM OLD.propagation_command_id
    OR NEW.propagation_payload_ref IS DISTINCT FROM OLD.propagation_payload_ref
    OR NEW.propagation_payload_hash IS DISTINCT FROM OLD.propagation_payload_hash
    OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'immutable run cancellation scope mutation is forbidden';
  END IF;
  IF OLD.propagation_complete AND (NOT NEW.propagation_complete OR NEW.propagation_cursor IS DISTINCT FROM OLD.propagation_cursor) THEN
    RAISE EXCEPTION 'completed cancellation propagation is immutable';
  END IF;
  IF OLD.propagation_cursor IS NOT NULL AND (NEW.propagation_cursor IS NULL OR NEW.propagation_cursor<OLD.propagation_cursor) THEN
    RAISE EXCEPTION 'cancellation propagation cursor must advance monotonically';
  END IF;
  IF NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at THEN
    RAISE EXCEPTION 'run cancellation version must advance exactly once';
  END IF;
  IF OLD.status='requested' AND NEW.status IN ('requested','terminating','settled') THEN RETURN NEW; END IF;
  IF OLD.status='terminating' AND NEW.status IN ('terminating','settled') THEN RETURN NEW; END IF;
  RAISE EXCEPTION 'invalid run cancellation transition % -> %',OLD.status,NEW.status;
END
$$;

CREATE TRIGGER run_cancellation_lifecycle
  BEFORE UPDATE OR DELETE ON agent.run_cancellations
  FOR EACH ROW EXECUTE FUNCTION agent.enforce_run_cancellation_lifecycle();
