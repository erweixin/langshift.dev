ALTER TABLE agent.runs
  DROP CONSTRAINT IF EXISTS runs_active_attempt_command_fk,
  DROP CONSTRAINT IF EXISTS runs_active_command_fk,
  DROP CONSTRAINT IF EXISTS runs_pending_command_fk;

ALTER TABLE agent.inbox
  DROP CONSTRAINT IF EXISTS inbox_lease_digest_contract;

ALTER TABLE agent.job_attempts
  DROP CONSTRAINT IF EXISTS job_attempts_tenant_job_command_fk,
  DROP CONSTRAINT IF EXISTS job_attempts_tenant_id_id_command_unique;

ALTER TABLE agent.jobs
  DROP CONSTRAINT IF EXISTS jobs_tenant_command_fk,
  DROP CONSTRAINT IF EXISTS jobs_tenant_id_id_command_unique,
  DROP CONSTRAINT IF EXISTS jobs_tenant_id_id_unique,
  DROP CONSTRAINT IF EXISTS jobs_status_contract;

ALTER TABLE agent.outbox
  DROP CONSTRAINT IF EXISTS outbox_tenant_command_unique;
