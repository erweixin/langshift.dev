ALTER TABLE agent.workspace_revision_commits
  ADD COLUMN approval_id uuid,
  ADD COLUMN approval_version bigint,
  ADD COLUMN approval_event_id uuid,
  ADD COLUMN proposal_hash text,
  ADD COLUMN commit_command_id uuid,
  ADD COLUMN published_hash text,
  ADD COLUMN observed_revision text,
  ADD COLUMN publish_attempt_id uuid,
  ADD COLUMN publish_fence bigint NOT NULL DEFAULT 0,
  ADD COLUMN publish_lease_hash bytea,
  ADD COLUMN publish_lease_expires_at timestamptz,
  ADD COLUMN authorized_at timestamptz,
  ADD COLUMN publishing_at timestamptz,
  ADD COLUMN outcome_unknown_at timestamptz,
  ADD COLUMN reconciliation_due_at timestamptz,
  ADD COLUMN reconciliation_attempts integer NOT NULL DEFAULT 0,
  ADD COLUMN failed_at timestamptz,
  ADD COLUMN abandoned_at timestamptz;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM agent.workspace_revision_commits) THEN
    RAISE EXCEPTION 'workspace revision protocol backfill required before migration 20' USING ERRCODE='55000';
  END IF;
END
$$;

ALTER TABLE agent.workspace_revision_commits
  DROP CONSTRAINT workspace_revision_status_contract,
  DROP CONSTRAINT workspace_revision_confirmation_contract,
  ADD CONSTRAINT workspace_revision_tenant_id_id_unique UNIQUE (tenant_id,id),
  ADD CONSTRAINT workspace_revision_command_unique UNIQUE (commit_command_id),
  ADD CONSTRAINT workspace_revision_status_contract CHECK (status IN ('prepared','authorized','publishing','confirmed','outcome_unknown','failed','abandoned')),
  ADD CONSTRAINT workspace_revision_scope_contract CHECK (
    NULLIF(base_revision,'') IS NOT NULL AND NULLIF(prepared_revision,'') IS NOT NULL
    AND NULLIF(prepared_hash,'') IS NOT NULL AND NULLIF(effect_key,'') IS NOT NULL
    AND publish_fence>=0 AND reconciliation_attempts>=0
  ),
  ADD CONSTRAINT workspace_revision_authorization_contract CHECK (
    (status='prepared' AND approval_id IS NULL AND approval_version IS NULL AND approval_event_id IS NULL
      AND proposal_hash IS NULL AND authorization_event_id IS NULL AND commit_command_id IS NULL AND authorized_at IS NULL)
    OR (status<>'prepared' AND approval_id IS NOT NULL AND approval_version>0 AND approval_event_id IS NOT NULL
      AND NULLIF(proposal_hash,'') IS NOT NULL AND authorization_event_id IS NOT NULL AND commit_command_id IS NOT NULL AND authorized_at IS NOT NULL)
    OR (status='abandoned' AND approval_id IS NULL AND approval_version IS NULL AND approval_event_id IS NULL
      AND proposal_hash IS NULL AND authorization_event_id IS NULL AND commit_command_id IS NULL AND authorized_at IS NULL)
  ),
  ADD CONSTRAINT workspace_revision_publish_contract CHECK (
    (status IN ('prepared','authorized') AND publish_attempt_id IS NULL AND publish_lease_hash IS NULL AND publish_lease_expires_at IS NULL AND publishing_at IS NULL)
    OR (status='publishing' AND publish_attempt_id IS NOT NULL AND publish_fence>0 AND octet_length(publish_lease_hash)=32 AND publish_lease_expires_at IS NOT NULL AND publishing_at IS NOT NULL)
    OR (status IN ('confirmed','outcome_unknown','failed') AND publish_attempt_id IS NOT NULL AND publish_fence>0 AND publish_lease_hash IS NULL AND publish_lease_expires_at IS NULL AND publishing_at IS NOT NULL)
    OR (status='abandoned' AND publish_lease_hash IS NULL AND publish_lease_expires_at IS NULL)
  ),
  ADD CONSTRAINT workspace_revision_terminal_contract CHECK (
    (status='confirmed' AND NULLIF(published_revision,'') IS NOT NULL AND NULLIF(published_hash,'') IS NOT NULL AND confirmed_at IS NOT NULL AND outcome_unknown_at IS NULL AND reconciliation_due_at IS NULL AND failed_at IS NULL AND abandoned_at IS NULL)
    OR (status='outcome_unknown' AND confirmed_at IS NULL AND outcome_unknown_at IS NOT NULL AND reconciliation_due_at IS NOT NULL AND failed_at IS NULL AND abandoned_at IS NULL)
    OR (status='failed' AND confirmed_at IS NULL AND reconciliation_due_at IS NULL AND failed_at IS NOT NULL AND abandoned_at IS NULL)
    OR (status='abandoned' AND confirmed_at IS NULL AND reconciliation_due_at IS NULL AND failed_at IS NULL AND abandoned_at IS NOT NULL)
    OR (status IN ('prepared','authorized','publishing') AND confirmed_at IS NULL AND outcome_unknown_at IS NULL AND reconciliation_due_at IS NULL AND failed_at IS NULL AND abandoned_at IS NULL)
  ),
  ADD CONSTRAINT workspace_revision_approval_fk FOREIGN KEY (tenant_id,approval_id) REFERENCES agent.approvals(tenant_id,id) ON DELETE RESTRICT,
  ADD CONSTRAINT workspace_revision_approval_event_fk FOREIGN KEY (approval_event_id) REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  ADD CONSTRAINT workspace_revision_authorization_event_fk FOREIGN KEY (authorization_event_id) REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;

CREATE FUNCTION agent.enforce_workspace_revision_lifecycle() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'workspace revision deletion is forbidden'; END IF;
  IF OLD.status IN ('confirmed','failed','abandoned') THEN RAISE EXCEPTION 'terminal workspace revision mutation is forbidden'; END IF;
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id OR NEW.created_at IS DISTINCT FROM OLD.created_at
    OR NEW.tool_call_id IS DISTINCT FROM OLD.tool_call_id OR NEW.workspace_id IS DISTINCT FROM OLD.workspace_id
    OR NEW.base_revision IS DISTINCT FROM OLD.base_revision OR NEW.prepared_revision IS DISTINCT FROM OLD.prepared_revision
    OR NEW.prepared_hash IS DISTINCT FROM OLD.prepared_hash OR NEW.effect_key IS DISTINCT FROM OLD.effect_key THEN
    RAISE EXCEPTION 'immutable workspace revision scope mutation is forbidden';
  END IF;
  IF OLD.status<>'prepared' AND (NEW.approval_id IS DISTINCT FROM OLD.approval_id OR NEW.approval_version IS DISTINCT FROM OLD.approval_version
    OR NEW.approval_event_id IS DISTINCT FROM OLD.approval_event_id OR NEW.proposal_hash IS DISTINCT FROM OLD.proposal_hash
    OR NEW.authorization_event_id IS DISTINCT FROM OLD.authorization_event_id OR NEW.authorized_at IS DISTINCT FROM OLD.authorized_at) THEN
    RAISE EXCEPTION 'workspace revision authorization mutation is forbidden';
  END IF;
  IF NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at THEN RAISE EXCEPTION 'workspace revision version must advance exactly once'; END IF;
  IF OLD.status='prepared' AND NEW.status IN ('authorized','abandoned') THEN RETURN NEW; END IF;
  IF OLD.status='authorized' AND NEW.status IN ('publishing','abandoned') THEN
    IF NEW.status='publishing' AND NEW.publish_fence<>OLD.publish_fence+1 THEN RAISE EXCEPTION 'workspace publish fence must advance'; END IF;
    RETURN NEW;
  END IF;
  IF OLD.status='publishing' AND NEW.status IN ('confirmed','outcome_unknown','failed') AND NEW.publish_fence=OLD.publish_fence THEN RETURN NEW; END IF;
  IF OLD.status='outcome_unknown' AND NEW.status IN ('authorized','confirmed','failed','abandoned') AND NEW.publish_fence=OLD.publish_fence THEN RETURN NEW; END IF;
  RAISE EXCEPTION 'invalid workspace revision transition % -> %',OLD.status,NEW.status;
END
$$;

CREATE TRIGGER workspace_revision_lifecycle BEFORE UPDATE OR DELETE ON agent.workspace_revision_commits
FOR EACH ROW EXECUTE FUNCTION agent.enforce_workspace_revision_lifecycle();

CREATE INDEX workspace_revision_recovery_idx ON agent.workspace_revision_commits(tenant_id,status,publish_lease_expires_at,reconciliation_due_at,id)
  WHERE status IN ('publishing','outcome_unknown');

CREATE OR REPLACE FUNCTION agent.list_workspace_recovery_tenants(
  p_store_epoch uuid,p_after uuid,p_limit integer,p_shard_index integer,p_shard_count integer,p_now timestamptz
) RETURNS TABLE(tenant_id text)
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,agent SET row_security=off AS $$
BEGIN
  IF p_store_epoch IS NULL OR p_limit IS NULL OR p_limit<1 OR p_limit>5000 OR p_shard_count IS NULL OR p_shard_count<1
    OR p_shard_index IS NULL OR p_shard_index<0 OR p_shard_index>=p_shard_count OR p_now IS NULL
  THEN RAISE EXCEPTION 'invalid workspace recovery tenant scan arguments' USING ERRCODE='22023'; END IF;
  RETURN QUERY SELECT w.tenant_id::text FROM agent.workspace_revision_commits w
    JOIN agent.events e ON e.tenant_id=w.tenant_id AND e.aggregate_kind='workspace_revision' AND e.aggregate_id=w.id
      AND e.aggregate_version=1 AND e.event_type='WorkspaceRevisionPrepared'
    WHERE e.store_epoch=p_store_epoch AND ((w.status='publishing' AND w.publish_lease_expires_at<=p_now)
      OR (w.status='outcome_unknown' AND w.reconciliation_due_at<=p_now))
      AND (p_after IS NULL OR w.tenant_id>p_after)
      AND ((hashtextextended(w.tenant_id::text,0) & 9223372036854775807) % p_shard_count)=p_shard_index
    GROUP BY w.tenant_id ORDER BY w.tenant_id LIMIT p_limit;
END
$$;

REVOKE ALL ON FUNCTION agent.list_workspace_recovery_tenants(uuid,uuid,integer,integer,integer,timestamptz) FROM PUBLIC;
