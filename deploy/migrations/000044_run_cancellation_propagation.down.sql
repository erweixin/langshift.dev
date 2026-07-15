DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM agent.run_cancellations WHERE parent_cancellation_id IS NOT NULL OR propagation_command_id IS NOT NULL) THEN
    RAISE EXCEPTION 'propagated cancellation facts prevent migration rollback';
  END IF;
END
$$;

DROP INDEX IF EXISTS agent.run_cancellations_propagation_idx;
DROP TRIGGER run_cancellation_event_guard ON agent.run_cancellations;
DROP FUNCTION agent.validate_run_cancellation_events();
DROP TRIGGER run_cancellation_lifecycle ON agent.run_cancellations;
DROP FUNCTION agent.enforce_run_cancellation_lifecycle();

ALTER TABLE agent.run_cancellations
  DROP CONSTRAINT run_cancellations_propagation_command_fk,
  DROP CONSTRAINT run_cancellations_propagation_contract,
  DROP CONSTRAINT run_cancellations_attempt_payload_contract,
  DROP COLUMN propagation_payload_hash,
  DROP COLUMN propagation_payload_ref,
  DROP COLUMN propagation_command_id,
  DROP COLUMN propagation_complete,
  DROP COLUMN propagation_cursor,
  DROP COLUMN attempt_cancelled_payload_hash,
  DROP COLUMN attempt_cancelled_payload_ref;

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
    OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'immutable run cancellation scope mutation is forbidden';
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
    SELECT 1 FROM agent.events e
    WHERE e.id=NEW.settlement_event_id AND e.tenant_id=NEW.tenant_id
      AND e.store_epoch=NEW.store_epoch AND e.aggregate_kind='run'
      AND e.aggregate_id=NEW.run_id AND e.event_type='RunCancelled'
  ) THEN RAISE EXCEPTION 'settled run cancellation lacks matching terminal event'; END IF;
  RETURN NULL;
END
$$;

CREATE CONSTRAINT TRIGGER run_cancellation_event_guard
  AFTER INSERT OR UPDATE ON agent.run_cancellations
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION agent.validate_run_cancellation_events();
