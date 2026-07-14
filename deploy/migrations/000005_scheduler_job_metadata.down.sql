DROP INDEX IF EXISTS agent.jobs_dispatch_lease_expiry_idx;
DROP INDEX IF EXISTS agent.jobs_scheduler_ready_idx;

ALTER TABLE agent.jobs
  DROP CONSTRAINT IF EXISTS jobs_dispatch_contract,
  DROP CONSTRAINT IF EXISTS jobs_retry_contract,
  DROP CONSTRAINT IF EXISTS jobs_cost_contract,
  DROP CONSTRAINT IF EXISTS jobs_priority_contract,
  DROP CONSTRAINT IF EXISTS jobs_resource_class_contract,
  DROP CONSTRAINT IF EXISTS jobs_queue_class_contract,
  DROP COLUMN IF EXISTS last_error_code,
  DROP COLUMN IF EXISTS dead_lettered_at,
  DROP COLUMN IF EXISTS last_dispatched_at,
  DROP COLUMN IF EXISTS dispatch_lease_expires_at,
  DROP COLUMN IF EXISTS dispatch_lease_hash,
  DROP COLUMN IF EXISTS dispatch_version,
  DROP COLUMN IF EXISTS max_attempts,
  DROP COLUMN IF EXISTS retry_count,
  DROP COLUMN IF EXISTS enqueued_at,
  DROP COLUMN IF EXISTS cost_units,
  DROP COLUMN IF EXISTS resource_class;
