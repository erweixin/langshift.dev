DROP TRIGGER IF EXISTS job_attempts_append_only ON agent.job_attempts;

CREATE FUNCTION agent.enforce_job_attempt_lifecycle() RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'job attempt deletion is forbidden';
  END IF;
  IF OLD.status <> 'running' THEN
    RAISE EXCEPTION 'terminal job attempt mutation is forbidden';
  END IF;
  IF NEW.id IS DISTINCT FROM OLD.id
    OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.created_at IS DISTINCT FROM OLD.created_at
    OR NEW.job_id IS DISTINCT FROM OLD.job_id
    OR NEW.command_id IS DISTINCT FROM OLD.command_id
    OR NEW.fence IS DISTINCT FROM OLD.fence
    OR NEW.lease_token_hash IS DISTINCT FROM OLD.lease_token_hash
    OR NEW.worker_id IS DISTINCT FROM OLD.worker_id
    OR NEW.started_at IS DISTINCT FROM OLD.started_at THEN
    RAISE EXCEPTION 'immutable job attempt identity or execution right mutation is forbidden';
  END IF;
  IF NEW.version <> OLD.version + 1 OR NEW.updated_at < OLD.updated_at THEN
    RAISE EXCEPTION 'job attempt version must advance exactly once';
  END IF;
  IF NEW.status = 'running' THEN
    IF NEW.lease_expires_at <= OLD.lease_expires_at
      OR NEW.finished_at IS NOT NULL
      OR NEW.result_hash IS NOT NULL THEN
      RAISE EXCEPTION 'invalid job attempt heartbeat';
    END IF;
  ELSIF NEW.status IN ('succeeded','failed','abandoned','expired') THEN
    IF NEW.lease_expires_at IS DISTINCT FROM OLD.lease_expires_at OR NEW.finished_at IS NULL THEN
      RAISE EXCEPTION 'invalid job attempt terminal transition';
    END IF;
  ELSE
    RAISE EXCEPTION 'invalid job attempt status';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER job_attempts_append_only BEFORE UPDATE OR DELETE ON agent.job_attempts FOR EACH ROW EXECUTE FUNCTION agent.enforce_job_attempt_lifecycle();

ALTER TABLE agent.runs
  ADD CONSTRAINT runs_status_contract CHECK (status IN ('accepted','queued','executing','waiting_tool','waiting_child','waiting_approval','succeeded','failed','cancelled','expired')),
  ADD CONSTRAINT runs_versions_contract CHECK (run_version > 0 AND current_fence >= 0),
  ADD CONSTRAINT runs_execution_right_contract CHECK (
    (status = 'queued' AND pending_command_id IS NOT NULL AND active_command_id IS NULL AND active_attempt_id IS NULL AND lease_token_hash IS NULL AND lease_expires_at IS NULL)
    OR
    (status = 'executing' AND pending_command_id IS NULL AND active_command_id IS NOT NULL AND active_attempt_id IS NOT NULL AND current_fence > 0 AND octet_length(lease_token_hash) = 32 AND lease_expires_at IS NOT NULL)
    OR
    (status NOT IN ('queued','executing') AND pending_command_id IS NULL AND active_command_id IS NULL AND active_attempt_id IS NULL AND lease_token_hash IS NULL AND lease_expires_at IS NULL)
  ),
  ADD CONSTRAINT runs_tenant_id_id_unique UNIQUE (tenant_id,id);

ALTER TABLE agent.tool_calls
  ADD CONSTRAINT tool_calls_status_contract CHECK (status IN ('requested','preview_requested','preparing_approval','awaiting_approval','executing','commit_requested','committing','outcome_unknown','succeeded','failed','cancelled','resolved_unknown')),
  ADD CONSTRAINT tool_calls_versions_contract CHECK (tool_call_version > 0 AND current_fence >= 0),
  ADD CONSTRAINT tool_calls_effect_class_contract CHECK (effect_class IN ('read_only','idempotent_write','reconcilable_write','compensatable_write','irreversible_write')),
  ADD CONSTRAINT tool_calls_effect_key_contract CHECK (effect_class = 'read_only' OR NULLIF(effect_key,'') IS NOT NULL),
  ADD CONSTRAINT tool_calls_execution_right_contract CHECK (
    (status IN ('requested','preview_requested','commit_requested') AND pending_command_id IS NOT NULL AND active_command_id IS NULL AND active_attempt_id IS NULL AND lease_token_hash IS NULL AND lease_expires_at IS NULL)
    OR
    (status IN ('preparing_approval','executing','committing') AND pending_command_id IS NULL AND active_command_id IS NOT NULL AND active_attempt_id IS NOT NULL AND current_fence > 0 AND octet_length(lease_token_hash) = 32 AND lease_expires_at IS NOT NULL)
    OR
    (status NOT IN ('requested','preview_requested','commit_requested','preparing_approval','executing','committing') AND pending_command_id IS NULL AND active_command_id IS NULL AND active_attempt_id IS NULL AND lease_token_hash IS NULL AND lease_expires_at IS NULL)
  ),
  ADD CONSTRAINT tool_calls_tenant_id_id_unique UNIQUE (tenant_id,id),
  ADD CONSTRAINT tool_calls_tenant_run_fk FOREIGN KEY (tenant_id,run_id) REFERENCES agent.runs(tenant_id,id) ON DELETE CASCADE;

ALTER TABLE agent.job_attempts
  ADD CONSTRAINT job_attempts_status_contract CHECK (status IN ('running','succeeded','failed','abandoned','expired')),
  ADD CONSTRAINT job_attempts_lifecycle_contract CHECK (
    fence > 0 AND octet_length(lease_token_hash) = 32 AND lease_expires_at > started_at
    AND ((status = 'running' AND finished_at IS NULL) OR (status <> 'running' AND finished_at IS NOT NULL))
  ),
  ADD CONSTRAINT job_attempts_job_fence_unique UNIQUE (tenant_id,job_id,fence);

ALTER TABLE agent.approvals
  ADD CONSTRAINT approvals_status_contract CHECK (status IN ('pending','granted','rejected','expired','invalidated')),
  ADD CONSTRAINT approvals_target_contract CHECK (
    target_version > 0
    AND (
      (approval_kind = 'tool_execution' AND run_id IS NOT NULL AND tool_call_id IS NOT NULL)
      OR (approval_kind = 'stage_checkpoint' AND run_id IS NOT NULL AND tool_call_id IS NULL)
    )
  ),
  ADD CONSTRAINT approvals_tenant_run_fk FOREIGN KEY (tenant_id,run_id) REFERENCES agent.runs(tenant_id,id) ON DELETE CASCADE,
  ADD CONSTRAINT approvals_tenant_tool_call_fk FOREIGN KEY (tenant_id,tool_call_id) REFERENCES agent.tool_calls(tenant_id,id) ON DELETE CASCADE;

ALTER TABLE agent.workspace_revision_commits
  ADD CONSTRAINT workspace_revision_status_contract CHECK (status IN ('prepared','authorized','publishing','confirmed','outcome_unknown','failed','abandoned')),
  ADD CONSTRAINT workspace_revision_confirmation_contract CHECK (
    (status = 'confirmed' AND published_revision IS NOT NULL AND confirmed_at IS NOT NULL)
    OR (status <> 'confirmed' AND confirmed_at IS NULL)
  ),
  ADD CONSTRAINT workspace_revision_tenant_tool_call_fk FOREIGN KEY (tenant_id,tool_call_id) REFERENCES agent.tool_calls(tenant_id,id) ON DELETE CASCADE;

ALTER TABLE agent.tool_effects
  ADD COLUMN tool_call_id uuid NOT NULL,
  ADD COLUMN run_id uuid NOT NULL,
  ADD COLUMN provider_request_id text,
  ADD COLUMN result_event_id uuid,
  ADD CONSTRAINT tool_effects_status_contract CHECK (status IN ('prepared','executing','commit_authorized','confirmed','failed','outcome_unknown','accepted_unknown')),
  ADD CONSTRAINT tool_effects_result_contract CHECK (
    (status = 'confirmed' AND confirmed_at IS NOT NULL)
    OR (status = 'accepted_unknown' AND residual_risk_ref IS NOT NULL)
    OR (status NOT IN ('confirmed','accepted_unknown') AND confirmed_at IS NULL)
  ),
  ADD CONSTRAINT tool_effects_tenant_run_fk FOREIGN KEY (tenant_id,run_id) REFERENCES agent.runs(tenant_id,id) ON DELETE CASCADE,
  ADD CONSTRAINT tool_effects_tenant_tool_call_fk FOREIGN KEY (tenant_id,tool_call_id) REFERENCES agent.tool_calls(tenant_id,id) ON DELETE CASCADE,
  ADD CONSTRAINT tool_effects_result_event_fk FOREIGN KEY (result_event_id) REFERENCES agent.events(id) ON DELETE RESTRICT;

CREATE INDEX runs_sweeper_due_idx ON agent.runs(tenant_id,due_at,id) WHERE status IN ('accepted','queued','executing','waiting_tool','waiting_child','waiting_approval');
CREATE INDEX job_attempts_expired_lease_idx ON agent.job_attempts(tenant_id,lease_expires_at,id) WHERE status = 'running';
CREATE INDEX approvals_expiry_idx ON agent.approvals(tenant_id,expires_at,id) WHERE status IN ('pending','granted');
CREATE INDEX tool_effects_reconcile_idx ON agent.tool_effects(tenant_id,reconciliation_due_at,id) WHERE status = 'outcome_unknown';
