CREATE OR REPLACE FUNCTION agent.scheduler_list_ready_jobs(
  p_resource_class text,p_owner text,p_claim_version bigint,p_lease_hash bytea,p_store_epoch uuid,p_now timestamptz,p_limit integer
) RETURNS TABLE(
  job_id text,tenant_id text,command_id text,queue_class text,priority integer,cost_units bigint,
  enqueued_at timestamptz,available_at timestamptz,due_at timestamptz,retry_count integer,dispatch_version bigint,
  outbox_id text,command_type text,aggregate_kind text,aggregate_id text,store_epoch text,payload_ref text,payload_hash text
)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, agent SET row_security = off AS $$
BEGIN
  IF p_store_epoch IS NULL OR p_now IS NULL OR p_limit IS NULL OR p_limit < 1 OR p_limit > 10000
    OR NOT EXISTS (SELECT 1 FROM agent.scheduler_resources r WHERE r.resource_class=p_resource_class AND r.version=p_claim_version AND r.lease_owner=p_owner AND r.lease_token_hash=p_lease_hash AND r.lease_expires_at>p_now) THEN
    RAISE EXCEPTION 'invalid or stale scheduler resource claim' USING ERRCODE = '40001';
  END IF;
  RETURN QUERY
    SELECT j.id::text,j.tenant_id::text,j.command_id::text,j.queue_class,j.priority,j.cost_units,
      j.enqueued_at,j.available_at,j.due_at,j.retry_count,j.dispatch_version,
      o.id::text,o.command_type,o.aggregate_kind,o.aggregate_id::text,o.store_epoch::text,o.payload_ref,o.payload_hash
    FROM agent.jobs j JOIN agent.outbox o ON o.tenant_id=j.tenant_id AND o.command_id=j.command_id
    WHERE j.resource_class=p_resource_class AND j.status='pending' AND j.available_at<=p_now
      AND (j.due_at IS NULL OR j.due_at>p_now) AND j.retry_count<j.max_attempts
      AND (j.dispatch_lease_hash IS NULL OR j.dispatch_lease_expires_at<=p_now)
      AND o.store_epoch=p_store_epoch AND o.status='published'
    ORDER BY j.enqueued_at,j.priority DESC,j.id LIMIT p_limit;
END
$$;

CREATE OR REPLACE FUNCTION agent.scheduler_commit_dispatch(
  p_resource_class text,p_owner text,p_claim_version bigint,p_resource_lease_hash bytea,p_scheduler_state jsonb,p_decisions jsonb,p_dispatch_expires_at timestamptz,p_now timestamptz
) RETURNS integer
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, agent SET row_security = off AS $$
DECLARE decision jsonb; updated_rows integer; committed integer := 0; job_lease_hash bytea;
BEGIN
  IF p_now IS NULL OR p_dispatch_expires_at IS NULL OR p_dispatch_expires_at<=p_now
    OR p_scheduler_state IS NULL OR jsonb_typeof(p_scheduler_state)<>'object' OR (p_scheduler_state->>'version')<>'1'
    OR octet_length(convert_to(p_scheduler_state::text,'UTF8'))>1048576
    OR p_decisions IS NULL OR jsonb_typeof(p_decisions)<>'array' OR jsonb_array_length(p_decisions)>1000 THEN
    RAISE EXCEPTION 'invalid scheduler dispatch commit' USING ERRCODE = '22023';
  END IF;
  PERFORM 1 FROM agent.scheduler_resources r WHERE r.resource_class=p_resource_class AND r.version=p_claim_version AND r.lease_owner=p_owner AND r.lease_token_hash=p_resource_lease_hash AND r.lease_expires_at>p_now FOR UPDATE;
  IF NOT FOUND THEN RAISE EXCEPTION 'stale scheduler resource claim' USING ERRCODE = '40001'; END IF;
  FOR decision IN SELECT value FROM jsonb_array_elements(p_decisions) LOOP
    IF jsonb_typeof(decision)<>'object' THEN RAISE EXCEPTION 'invalid scheduler decision' USING ERRCODE = '22023'; END IF;
    job_lease_hash := decode(decision->>'lease_hash','hex');
    IF octet_length(job_lease_hash)<>32 THEN RAISE EXCEPTION 'invalid scheduler job lease hash' USING ERRCODE = '22023'; END IF;
    UPDATE agent.jobs SET dispatch_version=dispatch_version+1,dispatch_lease_hash=job_lease_hash,dispatch_lease_expires_at=p_dispatch_expires_at,updated_at=p_now
      WHERE id=(decision->>'job_id')::uuid AND tenant_id=(decision->>'tenant_id')::uuid AND resource_class=p_resource_class
        AND dispatch_version=(decision->>'dispatch_version')::bigint AND status='pending' AND available_at<=p_now
        AND (due_at IS NULL OR due_at>p_now) AND retry_count<max_attempts AND (dispatch_lease_hash IS NULL OR dispatch_lease_expires_at<=p_now);
    GET DIAGNOSTICS updated_rows = ROW_COUNT;
    IF updated_rows<>1 THEN RAISE EXCEPTION 'scheduler decision conflict' USING ERRCODE = '40001'; END IF;
    committed := committed+1;
  END LOOP;
  UPDATE agent.scheduler_resources SET scheduler_state=p_scheduler_state,lease_owner=NULL,lease_token_hash=NULL,lease_expires_at=NULL,updated_at=p_now
    WHERE resource_class=p_resource_class AND version=p_claim_version AND lease_owner=p_owner AND lease_token_hash=p_resource_lease_hash AND lease_expires_at>p_now;
  IF NOT FOUND THEN RAISE EXCEPTION 'scheduler resource release conflict' USING ERRCODE = '40001'; END IF;
  RETURN committed;
END
$$;

DROP FUNCTION IF EXISTS agent.list_command_reconciliation_tenants(uuid,uuid,integer,integer,integer,timestamptz,timestamptz);
DROP INDEX IF EXISTS agent.jobs_delivery_due_idx;
DROP INDEX IF EXISTS agent.jobs_redelivery_ready_idx;
ALTER TABLE agent.jobs DROP CONSTRAINT IF EXISTS jobs_redelivery_contract;
ALTER TABLE agent.jobs
  DROP COLUMN IF EXISTS redelivery_reason,
  DROP COLUMN IF EXISTS redelivery_requested_at,
  DROP COLUMN IF EXISTS redelivery_count,
  DROP COLUMN IF EXISTS queue_generation,
  DROP COLUMN IF EXISTS delivery_due_at;
