ALTER TABLE agent.jobs
  ADD COLUMN resource_class text NOT NULL DEFAULT 'llm',
  ADD COLUMN cost_units bigint NOT NULL DEFAULT 1,
  ADD COLUMN enqueued_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  ADD COLUMN retry_count integer NOT NULL DEFAULT 0,
  ADD COLUMN max_attempts integer NOT NULL DEFAULT 5,
  ADD COLUMN dispatch_version bigint NOT NULL DEFAULT 0,
  ADD COLUMN dispatch_lease_hash bytea,
  ADD COLUMN dispatch_lease_expires_at timestamptz,
  ADD COLUMN last_dispatched_at timestamptz,
  ADD COLUMN dead_lettered_at timestamptz,
  ADD COLUMN last_error_code text,
  ADD CONSTRAINT jobs_queue_class_contract CHECK (queue_class IN ('interactive','background')),
  ADD CONSTRAINT jobs_resource_class_contract CHECK (resource_class <> '' AND length(resource_class) <= 128),
  ADD CONSTRAINT jobs_priority_contract CHECK (priority BETWEEN 0 AND 1000),
  ADD CONSTRAINT jobs_cost_contract CHECK (cost_units BETWEEN 1 AND 1000000000000),
  ADD CONSTRAINT jobs_retry_contract CHECK (retry_count >= 0 AND max_attempts BETWEEN 1 AND 100 AND retry_count <= max_attempts),
  ADD CONSTRAINT jobs_dispatch_contract CHECK (
    dispatch_version >= 0
    AND ((dispatch_lease_hash IS NULL AND dispatch_lease_expires_at IS NULL)
      OR (octet_length(dispatch_lease_hash) = 32 AND dispatch_lease_expires_at IS NOT NULL))
    AND ((status = 'dead_letter' AND dead_lettered_at IS NOT NULL)
      OR (status <> 'dead_letter' AND dead_lettered_at IS NULL))
  );

CREATE INDEX jobs_scheduler_ready_idx
  ON agent.jobs(resource_class,queue_class,available_at,enqueued_at,priority DESC,id)
  WHERE status = 'pending';

CREATE INDEX jobs_dispatch_lease_expiry_idx
  ON agent.jobs(dispatch_lease_expires_at,id)
  WHERE status = 'pending' AND dispatch_lease_hash IS NOT NULL;
