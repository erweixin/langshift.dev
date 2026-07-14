ALTER TABLE agent.jobs
  ADD COLUMN delivery_due_at timestamptz,
  ADD COLUMN queue_generation bigint NOT NULL DEFAULT 1,
  ADD COLUMN redelivery_count integer NOT NULL DEFAULT 0,
  ADD COLUMN redelivery_requested_at timestamptz,
  ADD COLUMN redelivery_reason text,
  ADD CONSTRAINT jobs_redelivery_contract CHECK (
    queue_generation = redelivery_count + 1
    AND redelivery_count BETWEEN 0 AND max_attempts
    AND ((redelivery_requested_at IS NULL AND redelivery_reason IS NULL)
      OR (redelivery_requested_at IS NOT NULL AND NULLIF(redelivery_reason,'') IS NOT NULL AND length(redelivery_reason) <= 128))
  );

CREATE INDEX jobs_redelivery_ready_idx
  ON agent.jobs(resource_class,redelivery_requested_at,enqueued_at,priority DESC,id)
  WHERE status IN ('pending','running') AND redelivery_requested_at IS NOT NULL;

CREATE INDEX jobs_delivery_due_idx
  ON agent.jobs(delivery_due_at,id)
  WHERE status IN ('pending','running') AND redelivery_requested_at IS NULL;

CREATE OR REPLACE FUNCTION agent.list_command_reconciliation_tenants(
  p_store_epoch uuid,
  p_after uuid,
  p_limit integer,
  p_shard_index integer,
  p_shard_count integer,
  p_now timestamptz,
  p_initial_published_before timestamptz
) RETURNS TABLE(tenant_id text)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, agent SET row_security = off AS $$
BEGIN
  IF p_store_epoch IS NULL OR p_limit IS NULL OR p_limit < 1 OR p_limit > 5000
    OR p_shard_count IS NULL OR p_shard_count < 1 OR p_shard_index IS NULL OR p_shard_index < 0 OR p_shard_index >= p_shard_count
    OR p_now IS NULL OR p_initial_published_before IS NULL OR p_initial_published_before > p_now THEN
    RAISE EXCEPTION 'invalid command reconciliation tenant scan arguments' USING ERRCODE = '22023';
  END IF;
  RETURN QUERY
    SELECT j.tenant_id::text
    FROM agent.jobs j
    JOIN agent.outbox o ON o.tenant_id=j.tenant_id AND o.command_id=j.command_id
    WHERE o.store_epoch=p_store_epoch AND o.status='published'
      AND j.status IN ('pending','running')
      AND j.redelivery_count<j.max_attempts
      AND j.redelivery_requested_at IS NULL
      AND (j.dispatch_lease_hash IS NULL OR j.dispatch_lease_expires_at<=p_now)
      AND (p_after IS NULL OR j.tenant_id>p_after)
      AND ((hashtextextended(j.tenant_id::text,0) & 9223372036854775807) % p_shard_count)=p_shard_index
      AND ((j.delivery_due_at IS NOT NULL AND j.delivery_due_at<=p_now)
        OR (j.delivery_due_at IS NULL AND o.published_at<=p_initial_published_before))
      AND (j.status='pending' OR EXISTS (
        SELECT 1 FROM agent.inbox i
        WHERE i.tenant_id=j.tenant_id AND i.command_id=j.command_id
          AND i.status='running' AND i.lease_expires_at<=p_now
      ))
    GROUP BY j.tenant_id
    ORDER BY j.tenant_id
    LIMIT p_limit;
END
$$;

REVOKE ALL ON FUNCTION agent.list_command_reconciliation_tenants(uuid,uuid,integer,integer,integer,timestamptz,timestamptz) FROM PUBLIC;

CREATE OR REPLACE FUNCTION agent.scheduler_list_ready_jobs(
  p_resource_class text,
  p_owner text,
  p_claim_version bigint,
  p_lease_hash bytea,
  p_store_epoch uuid,
  p_now timestamptz,
  p_limit integer
) RETURNS TABLE(
  job_id text,tenant_id text,command_id text,queue_class text,priority integer,cost_units bigint,
  enqueued_at timestamptz,available_at timestamptz,due_at timestamptz,retry_count integer,dispatch_version bigint,
  outbox_id text,command_type text,aggregate_kind text,aggregate_id text,store_epoch text,payload_ref text,payload_hash text
)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, agent SET row_security = off AS $$
BEGIN
  IF p_store_epoch IS NULL OR p_now IS NULL OR p_limit IS NULL OR p_limit < 1 OR p_limit > 10000
    OR NOT EXISTS (
      SELECT 1 FROM agent.scheduler_resources r
      WHERE r.resource_class=p_resource_class AND r.version=p_claim_version AND r.lease_owner=p_owner
        AND r.lease_token_hash=p_lease_hash AND r.lease_expires_at>p_now
    ) THEN
    RAISE EXCEPTION 'invalid or stale scheduler resource claim' USING ERRCODE = '40001';
  END IF;
  RETURN QUERY
    SELECT j.id::text,j.tenant_id::text,j.command_id::text,j.queue_class,j.priority,j.cost_units,
      j.enqueued_at,j.available_at,j.due_at,j.retry_count,j.dispatch_version,
      o.id::text,o.command_type,o.aggregate_kind,o.aggregate_id::text,o.store_epoch::text,o.payload_ref,o.payload_hash
    FROM agent.jobs j
    JOIN agent.outbox o ON o.tenant_id=j.tenant_id AND o.command_id=j.command_id
    WHERE j.resource_class=p_resource_class
      AND ((j.status='pending' AND j.redelivery_requested_at IS NULL AND j.available_at<=p_now)
        OR (j.status IN ('pending','running') AND j.redelivery_requested_at<=p_now))
      AND (j.due_at IS NULL OR j.due_at>p_now) AND j.retry_count<j.max_attempts AND j.redelivery_count<=j.max_attempts
      AND (j.dispatch_lease_hash IS NULL OR j.dispatch_lease_expires_at<=p_now)
      AND o.store_epoch=p_store_epoch AND o.status='published'
    ORDER BY j.enqueued_at,j.priority DESC,j.id
    LIMIT p_limit;
END
$$;

CREATE OR REPLACE FUNCTION agent.scheduler_commit_dispatch(
  p_resource_class text,
  p_owner text,
  p_claim_version bigint,
  p_resource_lease_hash bytea,
  p_scheduler_state jsonb,
  p_decisions jsonb,
  p_dispatch_expires_at timestamptz,
  p_now timestamptz
) RETURNS integer
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, agent SET row_security = off AS $$
DECLARE
  decision jsonb;
  updated_rows integer;
  committed integer := 0;
  job_lease_hash bytea;
BEGIN
  IF p_now IS NULL OR p_dispatch_expires_at IS NULL OR p_dispatch_expires_at<=p_now
    OR p_scheduler_state IS NULL OR jsonb_typeof(p_scheduler_state)<>'object' OR (p_scheduler_state->>'version')<>'1'
    OR octet_length(convert_to(p_scheduler_state::text,'UTF8'))>1048576
    OR p_decisions IS NULL OR jsonb_typeof(p_decisions)<>'array' OR jsonb_array_length(p_decisions)>1000 THEN
    RAISE EXCEPTION 'invalid scheduler dispatch commit' USING ERRCODE = '22023';
  END IF;
  PERFORM 1 FROM agent.scheduler_resources r
    WHERE r.resource_class=p_resource_class AND r.version=p_claim_version AND r.lease_owner=p_owner
      AND r.lease_token_hash=p_resource_lease_hash AND r.lease_expires_at>p_now
    FOR UPDATE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'stale scheduler resource claim' USING ERRCODE = '40001';
  END IF;
  FOR decision IN SELECT value FROM jsonb_array_elements(p_decisions)
  LOOP
    IF jsonb_typeof(decision)<>'object' THEN
      RAISE EXCEPTION 'invalid scheduler decision' USING ERRCODE = '22023';
    END IF;
    job_lease_hash := decode(decision->>'lease_hash','hex');
    IF octet_length(job_lease_hash)<>32 THEN
      RAISE EXCEPTION 'invalid scheduler job lease hash' USING ERRCODE = '22023';
    END IF;
    UPDATE agent.jobs
      SET dispatch_version=dispatch_version+1,dispatch_lease_hash=job_lease_hash,
          dispatch_lease_expires_at=p_dispatch_expires_at,updated_at=p_now
      WHERE id=(decision->>'job_id')::uuid AND tenant_id=(decision->>'tenant_id')::uuid
        AND resource_class=p_resource_class AND dispatch_version=(decision->>'dispatch_version')::bigint
        AND ((status='pending' AND redelivery_requested_at IS NULL AND available_at<=p_now)
          OR (status IN ('pending','running') AND redelivery_requested_at<=p_now))
        AND (due_at IS NULL OR due_at>p_now) AND retry_count<max_attempts AND redelivery_count<=max_attempts
        AND (dispatch_lease_hash IS NULL OR dispatch_lease_expires_at<=p_now);
    GET DIAGNOSTICS updated_rows = ROW_COUNT;
    IF updated_rows<>1 THEN
      RAISE EXCEPTION 'scheduler decision conflict' USING ERRCODE = '40001';
    END IF;
    committed := committed+1;
  END LOOP;
  UPDATE agent.scheduler_resources
    SET scheduler_state=p_scheduler_state,lease_owner=NULL,lease_token_hash=NULL,lease_expires_at=NULL,updated_at=p_now
    WHERE resource_class=p_resource_class AND version=p_claim_version AND lease_owner=p_owner
      AND lease_token_hash=p_resource_lease_hash AND lease_expires_at>p_now;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'scheduler resource release conflict' USING ERRCODE = '40001';
  END IF;
  RETURN committed;
END
$$;
