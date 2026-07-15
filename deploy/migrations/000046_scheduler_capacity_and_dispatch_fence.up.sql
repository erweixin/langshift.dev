CREATE FUNCTION agent.scheduler_active_counts(
  p_resource_class text,
  p_high_priority_threshold integer
) RETURNS TABLE(tenant_id text,queue_class text,high_priority boolean,active_count bigint)
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,agent SET row_security=off AS $$
BEGIN
  IF NULLIF(p_resource_class,'') IS NULL OR length(p_resource_class)>128
    OR p_high_priority_threshold IS NULL OR p_high_priority_threshold<0 OR p_high_priority_threshold>1000
  THEN
    RAISE EXCEPTION 'invalid scheduler active-count arguments' USING ERRCODE='22023';
  END IF;
  RETURN QUERY
    SELECT j.tenant_id::text,j.queue_class,j.priority>=p_high_priority_threshold,count(*)
    FROM agent.jobs j
    WHERE j.resource_class=p_resource_class AND j.status='running'
    GROUP BY j.tenant_id,j.queue_class,j.priority>=p_high_priority_threshold
    ORDER BY j.tenant_id,j.queue_class,j.priority>=p_high_priority_threshold;
END
$$;

CREATE FUNCTION agent.scheduler_list_ready_jobs_v2(
  p_resource_class text,
  p_owner text,
  p_claim_version bigint,
  p_lease_hash bytea,
  p_store_epoch uuid,
  p_now timestamptz,
  p_limit integer
) RETURNS TABLE(
  job_id text,tenant_id text,command_id text,queue_class text,priority integer,cost_units bigint,
  enqueued_at timestamptz,available_at timestamptz,due_at timestamptz,retry_count integer,
  dispatch_version bigint,queue_generation bigint,
  outbox_id text,command_type text,aggregate_kind text,aggregate_id text,store_epoch text,payload_ref text,payload_hash text
)
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,agent SET row_security=off AS $$
BEGIN
  IF p_store_epoch IS NULL OR p_now IS NULL OR p_limit IS NULL OR p_limit<1 OR p_limit>10000
    OR NOT EXISTS (
      SELECT 1 FROM agent.scheduler_resources r
      WHERE r.resource_class=p_resource_class AND r.version=p_claim_version AND r.lease_owner=p_owner
        AND r.lease_token_hash=p_lease_hash AND r.lease_expires_at>p_now
    )
  THEN
    RAISE EXCEPTION 'invalid or stale scheduler resource claim' USING ERRCODE='40001';
  END IF;
  RETURN QUERY
    SELECT j.id::text,j.tenant_id::text,j.command_id::text,j.queue_class,j.priority,j.cost_units,
      j.enqueued_at,j.available_at,j.due_at,j.retry_count,j.dispatch_version,j.queue_generation,
      o.id::text,o.command_type,o.aggregate_kind,o.aggregate_id::text,o.store_epoch::text,o.payload_ref,o.payload_hash
    FROM agent.jobs j
    JOIN agent.outbox o ON o.tenant_id=j.tenant_id AND o.command_id=j.command_id
    WHERE j.resource_class=p_resource_class
      AND ((j.status='pending' AND j.redelivery_requested_at IS NULL AND j.available_at<=p_now)
        OR (j.status IN ('pending','running') AND j.redelivery_requested_at<=p_now))
      AND (j.due_at IS NULL OR j.due_at>p_now) AND j.retry_count<j.max_attempts
      AND j.redelivery_count<=j.max_attempts
      AND (j.dispatch_lease_hash IS NULL OR j.dispatch_lease_expires_at<=p_now)
      AND o.store_epoch=p_store_epoch AND o.status='published'
    ORDER BY j.enqueued_at,j.priority DESC,j.id
    LIMIT p_limit;
END
$$;

REVOKE ALL ON FUNCTION agent.scheduler_active_counts(text,integer) FROM PUBLIC;
REVOKE ALL ON FUNCTION agent.scheduler_list_ready_jobs_v2(text,text,bigint,bytea,uuid,timestamptz,integer) FROM PUBLIC;
