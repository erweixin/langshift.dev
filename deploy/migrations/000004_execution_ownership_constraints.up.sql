ALTER TABLE agent.outbox
  ADD CONSTRAINT outbox_tenant_command_unique UNIQUE (tenant_id,command_id);

ALTER TABLE agent.jobs
  ADD CONSTRAINT jobs_status_contract CHECK (status IN ('pending','running','succeeded','failed','cancelled','expired','dead_letter')),
  ADD CONSTRAINT jobs_tenant_id_id_unique UNIQUE (tenant_id,id),
  ADD CONSTRAINT jobs_tenant_id_id_command_unique UNIQUE (tenant_id,id,command_id),
  ADD CONSTRAINT jobs_tenant_command_fk FOREIGN KEY (tenant_id,command_id)
    REFERENCES agent.outbox(tenant_id,command_id) DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE agent.job_attempts
  ADD CONSTRAINT job_attempts_tenant_id_id_command_unique UNIQUE (tenant_id,id,command_id),
  ADD CONSTRAINT job_attempts_tenant_job_command_fk FOREIGN KEY (tenant_id,job_id,command_id)
    REFERENCES agent.jobs(tenant_id,id,command_id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE agent.inbox
  ADD CONSTRAINT inbox_lease_digest_contract CHECK (octet_length(lease_token_hash) = 32);

ALTER TABLE agent.runs
  ADD CONSTRAINT runs_pending_command_fk FOREIGN KEY (tenant_id,pending_command_id)
    REFERENCES agent.outbox(tenant_id,command_id) DEFERRABLE INITIALLY DEFERRED,
  ADD CONSTRAINT runs_active_command_fk FOREIGN KEY (tenant_id,active_command_id)
    REFERENCES agent.outbox(tenant_id,command_id) DEFERRABLE INITIALLY DEFERRED,
  ADD CONSTRAINT runs_active_attempt_command_fk FOREIGN KEY (tenant_id,active_attempt_id,active_command_id)
    REFERENCES agent.job_attempts(tenant_id,id,command_id) DEFERRABLE INITIALLY DEFERRED;
