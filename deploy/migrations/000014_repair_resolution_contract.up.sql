ALTER TABLE agent.repair_commands
  ADD COLUMN resolution text,
  ADD COLUMN effect_version bigint,
  ADD COLUMN effect_key text,
  ADD COLUMN residual_risk_ref text,
  ADD COLUMN approved_at timestamptz,
  ADD COLUMN rejected_at timestamptz;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM agent.repair_commands) THEN
    RAISE EXCEPTION 'repair command backfill required before migration 14' USING ERRCODE='55000';
  END IF;
END
$$;

ALTER TABLE agent.repair_commands
  ALTER COLUMN resolution SET NOT NULL,
  ALTER COLUMN effect_version SET NOT NULL,
  ALTER COLUMN effect_key SET NOT NULL,
  ADD CONSTRAINT repair_commands_tenant_id_id_unique UNIQUE (tenant_id,id),
  ADD CONSTRAINT repair_commands_kind_contract CHECK (repair_kind='tool_effect_resolution' AND target_kind='tool_call'),
  ADD CONSTRAINT repair_commands_resolution_contract CHECK (resolution IN ('confirmed_occurred','confirmed_not_occurred','accepted_unknown')),
  ADD CONSTRAINT repair_commands_status_contract CHECK (status IN ('proposed','approved','rejected','executed','expired')),
  ADD CONSTRAINT repair_commands_scope_contract CHECK (
    target_version>0 AND effect_version>0 AND NULLIF(effect_key,'') IS NOT NULL
    AND NULLIF(proposal_hash,'') IS NOT NULL AND NULLIF(evidence_hash,'') IS NOT NULL
    AND ((resolution='accepted_unknown' AND NULLIF(residual_risk_ref,'') IS NOT NULL)
      OR (resolution<>'accepted_unknown' AND residual_risk_ref IS NULL))
  ),
  ADD CONSTRAINT repair_commands_lifecycle_contract CHECK (
    (status='proposed' AND approved_at IS NULL AND rejected_at IS NULL AND executed_at IS NULL)
    OR (status='approved' AND approved_at IS NOT NULL AND rejected_at IS NULL AND executed_at IS NULL)
    OR (status='rejected' AND approved_at IS NULL AND rejected_at IS NOT NULL AND executed_at IS NULL)
    OR (status='executed' AND approved_at IS NOT NULL AND rejected_at IS NULL AND executed_at IS NOT NULL)
    OR (status='expired' AND rejected_at IS NULL AND executed_at IS NULL)
  ),
  ADD CONSTRAINT repair_commands_initiator_fk FOREIGN KEY (initiator_user_id) REFERENCES identity.users(id) ON DELETE RESTRICT,
  ADD CONSTRAINT repair_commands_target_tool_fk FOREIGN KEY (tenant_id,target_id) REFERENCES agent.tool_calls(tenant_id,id) ON DELETE RESTRICT;

ALTER TABLE agent.repair_approvals
  ADD COLUMN decision text NOT NULL DEFAULT 'approve',
  ADD COLUMN effect_version bigint,
  ADD COLUMN approval_event_id uuid;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM agent.repair_approvals) THEN
    RAISE EXCEPTION 'repair approval backfill required before migration 14' USING ERRCODE='55000';
  END IF;
END
$$;

ALTER TABLE agent.repair_approvals
  DROP CONSTRAINT repair_approvals_repair_command_id_fk,
  ALTER COLUMN decision DROP DEFAULT,
  ALTER COLUMN effect_version SET NOT NULL,
  ALTER COLUMN approval_event_id SET NOT NULL,
  ADD CONSTRAINT repair_approvals_decision_contract CHECK (decision IN ('approve','reject')),
  ADD CONSTRAINT repair_approvals_scope_contract CHECK (target_version>0 AND effect_version>0 AND NULLIF(proposal_hash,'') IS NOT NULL AND NULLIF(permission_snapshot,'') IS NOT NULL AND reauthenticated_at<=approved_at),
  ADD CONSTRAINT repair_approvals_approver_fk FOREIGN KEY (approver_user_id) REFERENCES identity.users(id) ON DELETE RESTRICT,
  ADD CONSTRAINT repair_approvals_session_fk FOREIGN KEY (session_id) REFERENCES identity.sessions(id) ON DELETE RESTRICT,
  ADD CONSTRAINT repair_approvals_tenant_repair_fk FOREIGN KEY (tenant_id,repair_command_id) REFERENCES agent.repair_commands(tenant_id,id) ON DELETE CASCADE,
  ADD CONSTRAINT repair_approvals_event_fk FOREIGN KEY (approval_event_id) REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  ADD CONSTRAINT repair_approvals_event_unique UNIQUE (approval_event_id);

CREATE FUNCTION agent.enforce_repair_command_lifecycle() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'repair command deletion is forbidden'; END IF;
  IF OLD.status IN ('rejected','executed','expired') THEN RAISE EXCEPTION 'terminal repair command mutation is forbidden'; END IF;
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.created_at IS DISTINCT FROM OLD.created_at OR NEW.repair_kind IS DISTINCT FROM OLD.repair_kind
    OR NEW.target_kind IS DISTINCT FROM OLD.target_kind OR NEW.target_id IS DISTINCT FROM OLD.target_id
    OR NEW.target_version IS DISTINCT FROM OLD.target_version OR NEW.effect_version IS DISTINCT FROM OLD.effect_version
    OR NEW.effect_key IS DISTINCT FROM OLD.effect_key OR NEW.resolution IS DISTINCT FROM OLD.resolution
    OR NEW.proposal_hash IS DISTINCT FROM OLD.proposal_hash OR NEW.evidence_hash IS DISTINCT FROM OLD.evidence_hash
    OR NEW.residual_risk_ref IS DISTINCT FROM OLD.residual_risk_ref OR NEW.initiator_user_id IS DISTINCT FROM OLD.initiator_user_id
    OR NEW.expires_at IS DISTINCT FROM OLD.expires_at THEN
    RAISE EXCEPTION 'immutable repair scope mutation is forbidden';
  END IF;
  IF NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at THEN RAISE EXCEPTION 'repair version must advance exactly once'; END IF;
  IF OLD.status='proposed' AND NEW.status IN ('proposed','approved','rejected','expired') THEN RETURN NEW; END IF;
  IF OLD.status='approved' AND NEW.status IN ('executed','expired') THEN RETURN NEW; END IF;
  RAISE EXCEPTION 'invalid repair command transition % -> %',OLD.status,NEW.status;
END
$$;

CREATE TRIGGER repair_commands_lifecycle BEFORE UPDATE OR DELETE ON agent.repair_commands
FOR EACH ROW EXECUTE FUNCTION agent.enforce_repair_command_lifecycle();

CREATE INDEX repair_commands_pending_idx ON agent.repair_commands(tenant_id,status,expires_at,id)
  WHERE status IN ('proposed','approved');
