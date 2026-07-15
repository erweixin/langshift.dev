CREATE TABLE agent.child_group_cancellations (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  group_id uuid NOT NULL,
  parent_run_id uuid NOT NULL,
  root_run_id uuid NOT NULL,
  store_epoch uuid NOT NULL,
  status text NOT NULL DEFAULT 'pending',
  version bigint NOT NULL DEFAULT 1,
  cursor uuid,
  command_id uuid NOT NULL,
  payload_ref text NOT NULL,
  payload_hash text NOT NULL,
  available_at timestamptz NOT NULL,
  completed_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (tenant_id,id),
  CONSTRAINT child_group_cancellations_group_unique UNIQUE (tenant_id,group_id),
  CONSTRAINT child_group_cancellations_command_unique UNIQUE (tenant_id,command_id),
  CONSTRAINT child_group_cancellations_group_fk FOREIGN KEY (tenant_id,group_id)
    REFERENCES agent.child_groups(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT child_group_cancellations_parent_fk FOREIGN KEY (tenant_id,parent_run_id)
    REFERENCES agent.runs(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT child_group_cancellations_root_fk FOREIGN KEY (tenant_id,root_run_id)
    REFERENCES agent.runs(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT child_group_cancellations_command_fk FOREIGN KEY (tenant_id,command_id)
    REFERENCES agent.outbox(tenant_id,command_id) DEFERRABLE INITIALLY DEFERRED,
  CONSTRAINT child_group_cancellations_contract CHECK (
    status IN ('pending','completed') AND version>0
    AND NULLIF(payload_ref,'') IS NOT NULL AND payload_hash ~ '^[0-9a-f]{64}$'
    AND available_at>=created_at AND updated_at>=created_at
    AND ((status='pending' AND completed_at IS NULL)
      OR (status='completed' AND completed_at IS NOT NULL))
  )
);

ALTER TABLE agent.child_group_cancellations ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent.child_group_cancellations FORCE ROW LEVEL SECURITY;
CREATE POLICY child_group_cancellations_tenant_isolation ON agent.child_group_cancellations
  USING (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid);

CREATE FUNCTION agent.enforce_child_group_cancellation_lifecycle() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'child group cancellation deletion is forbidden'; END IF;
  IF OLD.status='completed' THEN RAISE EXCEPTION 'completed child group cancellation mutation is forbidden'; END IF;
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.group_id IS DISTINCT FROM OLD.group_id OR NEW.parent_run_id IS DISTINCT FROM OLD.parent_run_id
    OR NEW.root_run_id IS DISTINCT FROM OLD.root_run_id OR NEW.store_epoch IS DISTINCT FROM OLD.store_epoch
    OR NEW.command_id IS DISTINCT FROM OLD.command_id OR NEW.payload_ref IS DISTINCT FROM OLD.payload_ref
    OR NEW.payload_hash IS DISTINCT FROM OLD.payload_hash OR NEW.available_at IS DISTINCT FROM OLD.available_at
    OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'immutable child group cancellation scope mutation is forbidden';
  END IF;
  IF NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at THEN
    RAISE EXCEPTION 'child group cancellation version must advance exactly once';
  END IF;
  IF OLD.cursor IS NOT NULL AND (NEW.cursor IS NULL OR NEW.cursor<OLD.cursor) THEN
    RAISE EXCEPTION 'child group cancellation cursor must advance monotonically';
  END IF;
  IF OLD.status='pending' AND NEW.status IN ('pending','completed') THEN RETURN NEW; END IF;
  RAISE EXCEPTION 'invalid child group cancellation transition % -> %',OLD.status,NEW.status;
END
$$;

CREATE TRIGGER child_group_cancellation_lifecycle
  BEFORE UPDATE OR DELETE ON agent.child_group_cancellations
  FOR EACH ROW EXECUTE FUNCTION agent.enforce_child_group_cancellation_lifecycle();

CREATE INDEX child_group_cancellations_due_idx
  ON agent.child_group_cancellations(tenant_id,available_at,id)
  WHERE status='pending';

DROP FUNCTION agent.list_run_cancellation_tenants(uuid,uuid,integer,integer,integer,timestamptz);

CREATE FUNCTION agent.list_run_cancellation_tenants(
  p_store_epoch uuid,
  p_after uuid,
  p_limit integer,
  p_shard_index integer,
  p_shard_count integer,
  p_now timestamptz
) RETURNS TABLE(tenant_id text)
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,agent SET row_security=off AS $$
BEGIN
  IF p_store_epoch IS NULL OR p_limit IS NULL OR p_limit<1 OR p_limit>5000
    OR p_shard_count IS NULL OR p_shard_count<1 OR p_shard_index IS NULL
    OR p_shard_index<0 OR p_shard_index>=p_shard_count OR p_now IS NULL
  THEN
    RAISE EXCEPTION 'invalid run cancellation tenant scan arguments' USING ERRCODE='22023';
  END IF;

  RETURN QUERY
    SELECT work.tenant_id::text
    FROM (
      SELECT c.tenant_id
      FROM agent.run_cancellations c
      JOIN agent.events e ON e.tenant_id=c.tenant_id
        AND e.aggregate_kind='run_cancellation' AND e.aggregate_id=c.id
        AND e.event_type='RunCancellationRequested' AND e.aggregate_version=1
      WHERE c.store_epoch=p_store_epoch AND e.store_epoch=p_store_epoch
        AND c.status IN ('requested','terminating') AND c.reconciliation_due_at<=p_now
      UNION
      SELECT g.tenant_id
      FROM agent.child_group_cancellations g
      WHERE g.store_epoch=p_store_epoch AND g.status='pending' AND g.available_at<=p_now
    ) work
    WHERE (p_after IS NULL OR work.tenant_id>p_after)
      AND ((hashtextextended(work.tenant_id::text,0) & 9223372036854775807) % p_shard_count)=p_shard_index
    GROUP BY work.tenant_id
    ORDER BY work.tenant_id
    LIMIT p_limit;
END
$$;

REVOKE ALL ON FUNCTION agent.list_run_cancellation_tenants(uuid,uuid,integer,integer,integer,timestamptz) FROM PUBLIC;
