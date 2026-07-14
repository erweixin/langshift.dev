CREATE TABLE agent.scheduler_resources (
  resource_class text PRIMARY KEY,
  version bigint NOT NULL CHECK (version > 0),
  scheduler_state jsonb NOT NULL DEFAULT '{"version":1,"deficits":[],"buckets":[],"cursors":[]}'::jsonb,
  lease_owner text,
  lease_token_hash bytea,
  lease_expires_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CHECK (resource_class <> '' AND length(resource_class) <= 128),
  CHECK (jsonb_typeof(scheduler_state) = 'object' AND (scheduler_state->>'version') = '1'),
  CHECK ((lease_owner IS NULL AND lease_token_hash IS NULL AND lease_expires_at IS NULL)
    OR (lease_owner <> '' AND octet_length(lease_token_hash) = 32 AND lease_expires_at IS NOT NULL))
);

REVOKE ALL ON agent.scheduler_resources FROM PUBLIC;

CREATE FUNCTION agent.scheduler_claim_resource(
  p_resource_class text,
  p_owner text,
  p_lease_hash bytea,
  p_lease_expires_at timestamptz,
  p_now timestamptz
) RETURNS TABLE(claim_version bigint,scheduler_state jsonb)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, agent SET row_security = off AS $$
BEGIN
  IF p_resource_class IS NULL OR p_resource_class = '' OR length(p_resource_class) > 128
    OR p_owner IS NULL OR p_owner = '' OR length(p_owner) > 256
    OR octet_length(p_lease_hash) <> 32 OR p_now IS NULL
    OR p_lease_expires_at IS NULL OR p_lease_expires_at <= p_now THEN
    RAISE EXCEPTION 'invalid scheduler resource claim' USING ERRCODE = '22023';
  END IF;
  RETURN QUERY
    INSERT INTO agent.scheduler_resources(resource_class,version,lease_owner,lease_token_hash,lease_expires_at,created_at,updated_at)
    VALUES(p_resource_class,1,p_owner,p_lease_hash,p_lease_expires_at,p_now,p_now)
    ON CONFLICT (resource_class) DO UPDATE
      SET version=agent.scheduler_resources.version+1,
          lease_owner=EXCLUDED.lease_owner,
          lease_token_hash=EXCLUDED.lease_token_hash,
          lease_expires_at=EXCLUDED.lease_expires_at,
          updated_at=EXCLUDED.updated_at
      WHERE agent.scheduler_resources.lease_owner IS NULL OR agent.scheduler_resources.lease_expires_at<=p_now
    RETURNING agent.scheduler_resources.version,agent.scheduler_resources.scheduler_state;
END
$$;

CREATE FUNCTION agent.scheduler_list_ready_jobs(
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
    WHERE j.resource_class=p_resource_class AND j.status='pending' AND j.available_at<=p_now
      AND (j.due_at IS NULL OR j.due_at>p_now) AND j.retry_count<j.max_attempts
      AND (j.dispatch_lease_hash IS NULL OR j.dispatch_lease_expires_at<=p_now)
      AND o.store_epoch=p_store_epoch AND o.status='published'
    ORDER BY j.enqueued_at,j.priority DESC,j.id
    LIMIT p_limit;
END
$$;

CREATE FUNCTION agent.scheduler_commit_dispatch(
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
        AND status='pending' AND available_at<=p_now AND (due_at IS NULL OR due_at>p_now)
        AND retry_count<max_attempts
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

CREATE FUNCTION agent.scheduler_abort_resource(
  p_resource_class text,p_owner text,p_claim_version bigint,p_lease_hash bytea,p_now timestamptz
) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, agent SET row_security = off AS $$
BEGIN
  UPDATE agent.scheduler_resources
    SET lease_owner=NULL,lease_token_hash=NULL,lease_expires_at=NULL,updated_at=p_now
    WHERE resource_class=p_resource_class AND version=p_claim_version AND lease_owner=p_owner
      AND lease_token_hash=p_lease_hash;
  RETURN FOUND;
END
$$;

REVOKE ALL ON FUNCTION agent.scheduler_claim_resource(text,text,bytea,timestamptz,timestamptz) FROM PUBLIC;
REVOKE ALL ON FUNCTION agent.scheduler_list_ready_jobs(text,text,bigint,bytea,uuid,timestamptz,integer) FROM PUBLIC;
REVOKE ALL ON FUNCTION agent.scheduler_commit_dispatch(text,text,bigint,bytea,jsonb,jsonb,timestamptz,timestamptz) FROM PUBLIC;
REVOKE ALL ON FUNCTION agent.scheduler_abort_resource(text,text,bigint,bytea,timestamptz) FROM PUBLIC;
