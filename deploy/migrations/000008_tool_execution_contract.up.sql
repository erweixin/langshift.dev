ALTER TABLE agent.continuations
  ADD COLUMN IF NOT EXISTS continuation_kind text;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM agent.continuations WHERE continuation_kind IS NULL) THEN
    RAISE EXCEPTION 'tool execution v8 migration requires an explicit continuation_kind backfill before upgrade';
  END IF;
END
$$;

ALTER TABLE agent.tool_calls
  ADD COLUMN IF NOT EXISTS result_event_id uuid,
  ADD CONSTRAINT tool_calls_pending_command_fk FOREIGN KEY (tenant_id,pending_command_id)
    REFERENCES agent.outbox(tenant_id,command_id) DEFERRABLE INITIALLY DEFERRED,
  ADD CONSTRAINT tool_calls_active_command_fk FOREIGN KEY (tenant_id,active_command_id)
    REFERENCES agent.outbox(tenant_id,command_id) DEFERRABLE INITIALLY DEFERRED,
  ADD CONSTRAINT tool_calls_active_attempt_command_fk FOREIGN KEY (tenant_id,active_attempt_id,active_command_id)
    REFERENCES agent.job_attempts(tenant_id,id,command_id) DEFERRABLE INITIALLY DEFERRED,
  ADD CONSTRAINT tool_calls_result_event_fk FOREIGN KEY (result_event_id)
    REFERENCES agent.events(id) DEFERRABLE INITIALLY DEFERRED;

CREATE INDEX tool_calls_expired_lease_idx
  ON agent.tool_calls(tenant_id,lease_expires_at,id)
  WHERE status IN ('preparing_approval','executing','committing');

ALTER TABLE agent.parallel_groups
  ADD CONSTRAINT parallel_groups_tenant_run_fk FOREIGN KEY (tenant_id,run_id)
    REFERENCES agent.runs(tenant_id,id) ON DELETE CASCADE,
  ADD CONSTRAINT parallel_groups_joined_contract CHECK (
    (joined AND continuation_id IS NOT NULL)
    OR (NOT joined AND continuation_id IS NULL)
  );

ALTER TABLE agent.parallel_group_members
  ADD CONSTRAINT parallel_group_members_tool_once UNIQUE (tenant_id,tool_call_id);

ALTER TABLE agent.continuations
  DROP CONSTRAINT continuations_tenant_id_run_id_run_version_group_kind_group_key;

ALTER TABLE agent.continuations
  ALTER COLUMN continuation_kind SET NOT NULL,
  ADD CONSTRAINT continuations_version_contract CHECK (run_version>0),
  ADD CONSTRAINT continuations_group_kind_contract CHECK (group_kind IN ('parallel','child')),
  ADD CONSTRAINT continuations_kind_contract CHECK (
    continuation_kind IN ('resume','request_approval','resume_parent')
    AND (group_kind<>'parallel' OR continuation_kind IN ('resume','request_approval'))
    AND (group_kind<>'child' OR continuation_kind='resume_parent')
  ),
  ADD CONSTRAINT continuations_status_contract CHECK (status IN ('preparing','committed')),
  ADD CONSTRAINT continuations_tenant_id_id_unique UNIQUE (tenant_id,id),
  ADD CONSTRAINT continuations_group_once UNIQUE (tenant_id,run_id,group_kind,group_id,continuation_kind),
  ADD CONSTRAINT continuations_tenant_run_fk FOREIGN KEY (tenant_id,run_id)
    REFERENCES agent.runs(tenant_id,id) ON DELETE CASCADE,
  ADD CONSTRAINT continuations_command_fk FOREIGN KEY (tenant_id,command_id)
    REFERENCES agent.outbox(tenant_id,command_id) DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE agent.parallel_groups
  ADD CONSTRAINT parallel_groups_continuation_fk FOREIGN KEY (tenant_id,continuation_id)
    REFERENCES agent.continuations(tenant_id,id) DEFERRABLE INITIALLY DEFERRED;
