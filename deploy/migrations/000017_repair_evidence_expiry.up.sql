ALTER TABLE agent.repair_commands ADD COLUMN evidence_payload_ref text;

UPDATE agent.repair_commands
SET evidence_payload_ref=residual_risk_ref
WHERE residual_risk_ref IS NOT NULL;

DROP TRIGGER repair_commands_lifecycle ON agent.repair_commands;
DROP FUNCTION agent.enforce_repair_command_lifecycle();

ALTER TABLE agent.repair_commands
  DROP CONSTRAINT repair_commands_scope_contract,
  ADD CONSTRAINT repair_commands_scope_contract CHECK (
    target_version>0 AND effect_version>0 AND NULLIF(effect_key,'') IS NOT NULL
    AND NULLIF(proposal_hash,'') IS NOT NULL AND NULLIF(evidence_hash,'') IS NOT NULL
    AND (
      (repair_kind='tool_effect_resolution'
        AND ((resolution='accepted_unknown' AND NULLIF(residual_risk_ref,'') IS NOT NULL)
          OR (resolution<>'accepted_unknown' AND residual_risk_ref IS NULL)))
      OR
      (repair_kind='anonymous_claim_reconciliation'
        AND residual_risk_ref IS NULL AND NULLIF(evidence_payload_ref,'') IS NOT NULL)
    )
  );

CREATE FUNCTION agent.enforce_repair_command_lifecycle() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'repair command deletion is forbidden'; END IF;
  IF OLD.status IN ('rejected','executed','expired') THEN RAISE EXCEPTION 'terminal repair command mutation is forbidden'; END IF;
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.source_tenant_id IS DISTINCT FROM OLD.source_tenant_id
    OR NEW.tool_call_id IS DISTINCT FROM OLD.tool_call_id
    OR NEW.anonymous_claim_id IS DISTINCT FROM OLD.anonymous_claim_id
    OR NEW.created_at IS DISTINCT FROM OLD.created_at OR NEW.repair_kind IS DISTINCT FROM OLD.repair_kind
    OR NEW.target_kind IS DISTINCT FROM OLD.target_kind OR NEW.target_id IS DISTINCT FROM OLD.target_id
    OR NEW.target_version IS DISTINCT FROM OLD.target_version OR NEW.effect_version IS DISTINCT FROM OLD.effect_version
    OR NEW.effect_key IS DISTINCT FROM OLD.effect_key OR NEW.resolution IS DISTINCT FROM OLD.resolution
    OR NEW.proposal_hash IS DISTINCT FROM OLD.proposal_hash OR NEW.evidence_hash IS DISTINCT FROM OLD.evidence_hash
    OR NEW.evidence_payload_ref IS DISTINCT FROM OLD.evidence_payload_ref
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

CREATE OR REPLACE FUNCTION agent.list_expired_repair_tenants(
  p_store_epoch uuid,
  p_after uuid,
  p_limit integer,
  p_shard_index integer,
  p_shard_count integer,
  p_now timestamptz
) RETURNS TABLE(tenant_id text)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, agent SET row_security = off AS $$
BEGIN
  IF p_store_epoch IS NULL OR p_limit IS NULL OR p_limit < 1 OR p_limit > 5000
    OR p_shard_count IS NULL OR p_shard_count < 1 OR p_shard_index IS NULL OR p_shard_index < 0 OR p_shard_index >= p_shard_count
    OR p_now IS NULL THEN
    RAISE EXCEPTION 'invalid expired repair tenant scan arguments' USING ERRCODE='22023';
  END IF;
  RETURN QUERY
    SELECT r.tenant_id::text
    FROM agent.repair_commands r
    JOIN agent.events e ON e.tenant_id=r.tenant_id AND e.aggregate_kind='repair_command'
      AND e.aggregate_id=r.id AND e.aggregate_version=1 AND e.event_type='RepairCommandProposed'
    WHERE e.store_epoch=p_store_epoch AND r.status IN ('proposed','approved') AND r.expires_at<=p_now
      AND (p_after IS NULL OR r.tenant_id>p_after)
      AND ((hashtextextended(r.tenant_id::text,0) & 9223372036854775807) % p_shard_count)=p_shard_index
    GROUP BY r.tenant_id ORDER BY r.tenant_id LIMIT p_limit;
END
$$;

REVOKE ALL ON FUNCTION agent.list_expired_repair_tenants(uuid,uuid,integer,integer,integer,timestamptz) FROM PUBLIC;
