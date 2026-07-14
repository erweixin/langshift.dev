DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM agent.repair_commands WHERE repair_kind<>'tool_effect_resolution') THEN
    RAISE EXCEPTION 'migration 16 rollback cannot represent non-tool repair commands' USING ERRCODE='55000';
  END IF;
END
$$;

DROP INDEX IF EXISTS agent.repair_commands_anonymous_claim_idx;
DROP TRIGGER repair_commands_lifecycle ON agent.repair_commands;
DROP FUNCTION agent.enforce_repair_command_lifecycle();

ALTER TABLE agent.repair_commands
  DROP CONSTRAINT repair_commands_anonymous_claim_fk,
  DROP CONSTRAINT repair_commands_tool_call_fk,
  DROP CONSTRAINT repair_commands_scope_contract,
  DROP CONSTRAINT repair_commands_resolution_contract,
  DROP CONSTRAINT repair_commands_kind_contract,
  ADD CONSTRAINT repair_commands_kind_contract CHECK (repair_kind='tool_effect_resolution' AND target_kind='tool_call'),
  ADD CONSTRAINT repair_commands_resolution_contract CHECK (resolution IN ('confirmed_occurred','confirmed_not_occurred','accepted_unknown')),
  ADD CONSTRAINT repair_commands_scope_contract CHECK (
    target_version>0 AND effect_version>0 AND NULLIF(effect_key,'') IS NOT NULL
    AND NULLIF(proposal_hash,'') IS NOT NULL AND NULLIF(evidence_hash,'') IS NOT NULL
    AND ((resolution='accepted_unknown' AND NULLIF(residual_risk_ref,'') IS NOT NULL)
      OR (resolution<>'accepted_unknown' AND residual_risk_ref IS NULL))
  ),
  ADD CONSTRAINT repair_commands_target_tool_fk FOREIGN KEY (tenant_id,target_id)
    REFERENCES agent.tool_calls(tenant_id,id) ON DELETE RESTRICT;

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

ALTER TABLE agent.repair_commands
  DROP COLUMN anonymous_claim_id,
  DROP COLUMN tool_call_id,
  DROP COLUMN source_tenant_id;

ALTER TABLE identity.onboarding_claims
  DROP CONSTRAINT onboarding_claims_tenant_id_id_unique;
