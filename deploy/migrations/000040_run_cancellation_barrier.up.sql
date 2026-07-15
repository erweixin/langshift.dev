DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM agent.run_cancellations) THEN
    RAISE EXCEPTION 'run cancellation rows predate the event-backed barrier protocol';
  END IF;
END
$$;

ALTER TABLE agent.runs
  ADD COLUMN cancel_generation bigint NOT NULL DEFAULT 0,
  ADD COLUMN active_cancellation_id uuid,
  ADD CONSTRAINT runs_cancellation_contract CHECK (
    (cancel_requested_at IS NULL AND cancel_generation=0 AND active_cancellation_id IS NULL)
    OR (cancel_requested_at IS NOT NULL AND cancel_generation>0 AND active_cancellation_id IS NOT NULL)
  );

ALTER TABLE agent.run_cancellations
  DROP CONSTRAINT run_cancellations_run_id_fk,
  ADD COLUMN store_epoch uuid NOT NULL,
  ADD COLUMN request_hash text NOT NULL,
  ADD COLUMN request_event_id uuid NOT NULL,
  ADD COLUMN settlement_event_id uuid,
  ADD COLUMN request_payload_ref text NOT NULL,
  ADD COLUMN request_payload_hash text NOT NULL,
  ADD COLUMN settlement_payload_ref text NOT NULL,
  ADD COLUMN settlement_payload_hash text NOT NULL,
  ADD COLUMN reconciliation_due_at timestamptz NOT NULL,
  ADD CONSTRAINT run_cancellations_tenant_id_id_unique UNIQUE (tenant_id,id),
  ADD CONSTRAINT run_cancellations_generation_unique UNIQUE (tenant_id,run_id,cancel_generation),
  ADD CONSTRAINT run_cancellations_root_unique UNIQUE (tenant_id,run_id,root_cancellation_id),
  ADD CONSTRAINT run_cancellations_run_fk FOREIGN KEY (tenant_id,run_id)
    REFERENCES agent.runs(tenant_id,id) ON DELETE RESTRICT,
  ADD CONSTRAINT run_cancellations_request_event_fk FOREIGN KEY (request_event_id)
    REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  ADD CONSTRAINT run_cancellations_settlement_event_fk FOREIGN KEY (settlement_event_id)
    REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  ADD CONSTRAINT run_cancellations_scope_contract CHECK (
    cancel_generation>0
    AND status IN ('requested','terminating','settled')
    AND requested_at>=created_at
    AND reconciliation_due_at>=requested_at
    AND length(reason) BETWEEN 1 AND 1000
    AND request_hash ~ '^[0-9a-f]{64}$'
    AND NULLIF(request_payload_ref,'') IS NOT NULL
    AND request_payload_hash ~ '^[0-9a-f]{64}$'
    AND NULLIF(settlement_payload_ref,'') IS NOT NULL
    AND settlement_payload_hash ~ '^[0-9a-f]{64}$'
    AND ((parent_cancellation_id IS NULL AND root_cancellation_id=id)
      OR (parent_cancellation_id IS NOT NULL AND root_cancellation_id<>id))
    AND ((status='settled' AND settled_at IS NOT NULL AND settlement_event_id IS NOT NULL)
      OR (status<>'settled' AND settled_at IS NULL AND settlement_event_id IS NULL))
  );

ALTER TABLE agent.runs
  ADD CONSTRAINT runs_active_cancellation_fk FOREIGN KEY (tenant_id,active_cancellation_id)
    REFERENCES agent.run_cancellations(tenant_id,id) DEFERRABLE INITIALLY DEFERRED;

CREATE INDEX run_cancellations_reconcile_idx
  ON agent.run_cancellations(tenant_id,reconciliation_due_at,id)
  WHERE status IN ('requested','terminating');

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

CREATE FUNCTION agent.list_run_cancellation_tenants(
  p_anchor_tenant uuid,
  p_after_tenant uuid,
  p_shard_count integer,
  p_shard_index integer,
  p_limit integer,
  p_now timestamptz
) RETURNS TABLE(tenant_id uuid)
LANGUAGE sql STABLE SECURITY INVOKER SET search_path=pg_catalog,agent AS $$
  SELECT c.tenant_id
  FROM agent.run_cancellations c
  WHERE c.status IN ('requested','terminating') AND c.reconciliation_due_at<=p_now
    AND c.tenant_id<>p_anchor_tenant AND c.tenant_id>p_after_tenant
    AND mod(abs(hashtextextended(c.tenant_id::text,0)),p_shard_count)=p_shard_index
  GROUP BY c.tenant_id
  ORDER BY c.tenant_id
  LIMIT p_limit
$$;

REVOKE ALL ON FUNCTION agent.list_run_cancellation_tenants(uuid,uuid,integer,integer,integer,timestamptz) FROM PUBLIC;
