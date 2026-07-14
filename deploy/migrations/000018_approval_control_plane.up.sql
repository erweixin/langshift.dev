ALTER TABLE agent.approvals
  ADD COLUMN granted_at timestamptz,
  ADD COLUMN rejected_at timestamptz,
  ADD COLUMN invalidated_at timestamptz,
  ADD COLUMN expired_at timestamptz;

ALTER TABLE agent.approval_decisions
  ADD COLUMN decision_event_id uuid,
  ADD COLUMN decision_mode text;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM agent.approvals) OR EXISTS (SELECT 1 FROM agent.approval_decisions) THEN
    RAISE EXCEPTION 'approval control-plane backfill required before migration 18' USING ERRCODE='55000';
  END IF;
END
$$;

ALTER TABLE agent.approvals
  DROP CONSTRAINT approvals_status_contract,
  ADD CONSTRAINT approvals_tenant_id_id_unique UNIQUE (tenant_id,id),
  ADD CONSTRAINT approvals_status_contract CHECK (status IN ('pending','granted','rejected','expired','invalidated')),
  ADD CONSTRAINT approvals_scope_contract CHECK (
    target_version>0 AND NULLIF(proposal_hash,'') IS NOT NULL
    AND NULLIF(permission_snapshot,'') IS NOT NULL AND expires_at>created_at
  ),
  ADD CONSTRAINT approvals_lifecycle_contract CHECK (
    (status='pending' AND granted_at IS NULL AND rejected_at IS NULL AND invalidated_at IS NULL AND expired_at IS NULL)
    OR (status='granted' AND granted_at IS NOT NULL AND rejected_at IS NULL AND invalidated_at IS NULL AND expired_at IS NULL)
    OR (status='rejected' AND granted_at IS NULL AND rejected_at IS NOT NULL AND invalidated_at IS NULL AND expired_at IS NULL)
    OR (status='invalidated' AND rejected_at IS NULL AND invalidated_at IS NOT NULL AND expired_at IS NULL)
    OR (status='expired' AND rejected_at IS NULL AND invalidated_at IS NULL AND expired_at IS NOT NULL)
  ),
  ADD CONSTRAINT approvals_requested_by_fk FOREIGN KEY (requested_by) REFERENCES identity.users(id) ON DELETE RESTRICT;

ALTER TABLE agent.approval_decisions
  DROP CONSTRAINT approval_decisions_approval_id_fk,
  ALTER COLUMN decision_event_id SET NOT NULL,
  ALTER COLUMN decision_mode SET NOT NULL,
  ADD CONSTRAINT approval_decisions_decision_contract CHECK (decision IN ('approve','reject','revise')),
  ADD CONSTRAINT approval_decisions_mode_contract CHECK (decision_mode IN ('user','admin')),
  ADD CONSTRAINT approval_decisions_scope_contract CHECK (
    target_version>0 AND NULLIF(proposal_hash,'') IS NOT NULL
    AND NULLIF(permission_snapshot,'') IS NOT NULL AND reauthenticated_at<=decided_at
  ),
  ADD CONSTRAINT approval_decisions_actor_fk FOREIGN KEY (actor_user_id) REFERENCES identity.users(id) ON DELETE RESTRICT,
  ADD CONSTRAINT approval_decisions_session_fk FOREIGN KEY (session_id) REFERENCES identity.sessions(id) ON DELETE RESTRICT,
  ADD CONSTRAINT approval_decisions_tenant_approval_fk FOREIGN KEY (tenant_id,approval_id) REFERENCES agent.approvals(tenant_id,id) ON DELETE CASCADE,
  ADD CONSTRAINT approval_decisions_event_fk FOREIGN KEY (decision_event_id) REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  ADD CONSTRAINT approval_decisions_event_unique UNIQUE (decision_event_id);

CREATE FUNCTION agent.enforce_approval_lifecycle() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'approval deletion is forbidden'; END IF;
  IF OLD.status IN ('rejected','expired','invalidated') THEN RAISE EXCEPTION 'terminal approval mutation is forbidden'; END IF;
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.created_at IS DISTINCT FROM OLD.created_at OR NEW.run_id IS DISTINCT FROM OLD.run_id
    OR NEW.tool_call_id IS DISTINCT FROM OLD.tool_call_id OR NEW.approval_kind IS DISTINCT FROM OLD.approval_kind
    OR NEW.proposal_hash IS DISTINCT FROM OLD.proposal_hash OR NEW.target_version IS DISTINCT FROM OLD.target_version
    OR NEW.permission_snapshot IS DISTINCT FROM OLD.permission_snapshot OR NEW.expires_at IS DISTINCT FROM OLD.expires_at
    OR NEW.requested_by IS DISTINCT FROM OLD.requested_by THEN
    RAISE EXCEPTION 'immutable approval scope mutation is forbidden';
  END IF;
  IF NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at THEN RAISE EXCEPTION 'approval version must advance exactly once'; END IF;
  IF OLD.status='pending' AND NEW.status IN ('granted','rejected','invalidated','expired') THEN RETURN NEW; END IF;
  IF OLD.status='granted' AND NEW.status='invalidated' THEN RETURN NEW; END IF;
  RAISE EXCEPTION 'invalid approval transition % -> %',OLD.status,NEW.status;
END
$$;

CREATE TRIGGER approvals_lifecycle BEFORE UPDATE OR DELETE ON agent.approvals
FOR EACH ROW EXECUTE FUNCTION agent.enforce_approval_lifecycle();

CREATE INDEX approvals_pending_idx ON agent.approvals(tenant_id,status,expires_at,id)
  WHERE status IN ('pending','granted');

CREATE OR REPLACE FUNCTION agent.list_expired_approval_tenants(
  p_store_epoch uuid,
  p_after uuid,
  p_limit integer,
  p_shard_index integer,
  p_shard_count integer,
  p_now timestamptz
) RETURNS TABLE(tenant_id text)
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,agent SET row_security=off AS $$
BEGIN
  IF p_store_epoch IS NULL OR p_limit IS NULL OR p_limit<1 OR p_limit>5000
    OR p_shard_count IS NULL OR p_shard_count<1 OR p_shard_index IS NULL OR p_shard_index<0 OR p_shard_index>=p_shard_count
    OR p_now IS NULL THEN RAISE EXCEPTION 'invalid expired approval tenant scan arguments' USING ERRCODE='22023'; END IF;
  RETURN QUERY
    SELECT a.tenant_id::text FROM agent.approvals a
    JOIN agent.events e ON e.tenant_id=a.tenant_id AND e.aggregate_kind='approval'
      AND e.aggregate_id=a.id AND e.aggregate_version=1 AND e.event_type='ApprovalRequested'
    WHERE e.store_epoch=p_store_epoch AND a.status='pending' AND a.expires_at<=p_now
      AND (p_after IS NULL OR a.tenant_id>p_after)
      AND ((hashtextextended(a.tenant_id::text,0) & 9223372036854775807) % p_shard_count)=p_shard_index
    GROUP BY a.tenant_id ORDER BY a.tenant_id LIMIT p_limit;
END
$$;

REVOKE ALL ON FUNCTION agent.list_expired_approval_tenants(uuid,uuid,integer,integer,integer,timestamptz) FROM PUBLIC;
