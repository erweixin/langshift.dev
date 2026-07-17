DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM contracts.manual_adjustments) THEN
    RAISE EXCEPTION 'manual adjustment control-plane backfill required before migration 78' USING ERRCODE='55000';
  END IF;
END
$$;

CREATE TABLE contracts.accounting_adjustment_proposals (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version>0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  initiator_user_id uuid NOT NULL,
  initiator_membership_id uuid NOT NULL,
  initiator_session_id uuid NOT NULL,
  reauthenticated_at timestamptz NOT NULL,
  bucket_id uuid NOT NULL,
  target_bucket_version bigint NOT NULL CHECK (target_bucket_version>0),
  units bigint NOT NULL CHECK (units<>0),
  status text NOT NULL DEFAULT 'proposed',
  proposal_hash text NOT NULL,
  reason_ref text NOT NULL,
  reason_hash text NOT NULL,
  expires_at timestamptz NOT NULL,
  executed_at timestamptz,
  UNIQUE (tenant_id,id),
  UNIQUE (tenant_id,proposal_hash),
  CONSTRAINT accounting_adjustment_proposals_tenant_fk FOREIGN KEY (tenant_id)
    REFERENCES identity.tenants(id) ON DELETE RESTRICT,
  CONSTRAINT accounting_adjustment_proposals_membership_fk FOREIGN KEY (tenant_id,initiator_membership_id)
    REFERENCES identity.memberships(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT accounting_adjustment_proposals_session_fk FOREIGN KEY (initiator_session_id)
    REFERENCES identity.sessions(id) ON DELETE RESTRICT,
  CONSTRAINT accounting_adjustment_proposals_bucket_fk FOREIGN KEY (tenant_id,bucket_id)
    REFERENCES contracts.credit_buckets(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT accounting_adjustment_proposals_contract CHECK (
    status IN ('proposed','rejected','expired','executed')
    AND proposal_hash ~ '^[0-9a-f]{64}$' AND reason_hash ~ '^[0-9a-f]{64}$'
    AND length(btrim(reason_ref)) BETWEEN 1 AND 1000
    AND expires_at>created_at
    AND reauthenticated_at<=created_at AND reauthenticated_at>=created_at-interval '5 minutes'
    AND ((status='proposed' AND version=1 AND executed_at IS NULL)
      OR (status IN ('rejected','expired') AND version=2 AND executed_at IS NULL)
      OR (status='executed' AND version=2 AND executed_at IS NOT NULL))
  )
);
ALTER TABLE contracts.accounting_adjustment_proposals ENABLE ROW LEVEL SECURITY;
ALTER TABLE contracts.accounting_adjustment_proposals FORCE ROW LEVEL SECURITY;
CREATE POLICY accounting_adjustment_proposals_tenant_isolation ON contracts.accounting_adjustment_proposals
  USING (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid);

CREATE TABLE contracts.accounting_adjustment_approval_decisions (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  proposal_id uuid NOT NULL,
  approver_user_id uuid NOT NULL,
  membership_id uuid NOT NULL,
  session_id uuid NOT NULL,
  decision text NOT NULL,
  proposal_hash text NOT NULL,
  target_bucket_version bigint NOT NULL,
  permission_snapshot text NOT NULL,
  reauthenticated_at timestamptz NOT NULL,
  decided_at timestamptz NOT NULL,
  event_id uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE (tenant_id,proposal_id,approver_user_id),
  UNIQUE (tenant_id,proposal_id,session_id),
  UNIQUE (tenant_id,event_id),
  CONSTRAINT accounting_adjustment_approval_proposal_fk FOREIGN KEY (tenant_id,proposal_id)
    REFERENCES contracts.accounting_adjustment_proposals(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT accounting_adjustment_approval_membership_fk FOREIGN KEY (tenant_id,membership_id)
    REFERENCES identity.memberships(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT accounting_adjustment_approval_event_fk FOREIGN KEY (event_id)
    REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  CONSTRAINT accounting_adjustment_approval_contract CHECK (
    decision IN ('approve','reject') AND proposal_hash ~ '^[0-9a-f]{64}$'
    AND target_bucket_version>0 AND permission_snapshot ~ '^[0-9a-f]{64}$'
    AND reauthenticated_at<=decided_at AND reauthenticated_at>=decided_at-interval '5 minutes'
  )
);
ALTER TABLE contracts.accounting_adjustment_approval_decisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE contracts.accounting_adjustment_approval_decisions FORCE ROW LEVEL SECURITY;
CREATE POLICY accounting_adjustment_approval_tenant_isolation ON contracts.accounting_adjustment_approval_decisions
  USING (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid);
CREATE TRIGGER accounting_adjustment_approval_append_only BEFORE UPDATE OR DELETE
  ON contracts.accounting_adjustment_approval_decisions FOR EACH ROW EXECUTE FUNCTION agent.reject_append_only_mutation();

CREATE FUNCTION contracts.enforce_accounting_adjustment_proposal_lifecycle() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog,contracts AS $$
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'accounting adjustment proposal deletion is forbidden'; END IF;
  IF OLD.status<>'proposed' OR OLD.version<>1
    OR NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.initiator_user_id IS DISTINCT FROM OLD.initiator_user_id
    OR NEW.initiator_membership_id IS DISTINCT FROM OLD.initiator_membership_id
    OR NEW.initiator_session_id IS DISTINCT FROM OLD.initiator_session_id
    OR NEW.reauthenticated_at IS DISTINCT FROM OLD.reauthenticated_at
    OR NEW.bucket_id IS DISTINCT FROM OLD.bucket_id OR NEW.target_bucket_version IS DISTINCT FROM OLD.target_bucket_version
    OR NEW.units IS DISTINCT FROM OLD.units OR NEW.proposal_hash IS DISTINCT FROM OLD.proposal_hash
    OR NEW.reason_ref IS DISTINCT FROM OLD.reason_ref OR NEW.reason_hash IS DISTINCT FROM OLD.reason_hash
    OR NEW.expires_at IS DISTINCT FROM OLD.expires_at OR NEW.created_at IS DISTINCT FROM OLD.created_at
    OR NEW.version<>2 OR NEW.status NOT IN ('rejected','expired','executed') OR NEW.updated_at<OLD.updated_at
    OR (NEW.status='executed')<>(NEW.executed_at IS NOT NULL)
  THEN RAISE EXCEPTION 'invalid accounting adjustment proposal transition'; END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER accounting_adjustment_proposals_lifecycle BEFORE UPDATE OR DELETE
  ON contracts.accounting_adjustment_proposals FOR EACH ROW EXECUTE FUNCTION contracts.enforce_accounting_adjustment_proposal_lifecycle();

CREATE FUNCTION contracts.validate_accounting_adjustment_proposal() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,contracts,identity SET row_security=off AS $$
DECLARE valid_principal boolean; bucket_version bigint; granted bigint; reserved bigint; settled bigint;
BEGIN
  IF NEW.created_at<statement_timestamp()-interval '1 minute' OR NEW.created_at>statement_timestamp()+interval '1 minute' THEN
    RAISE EXCEPTION 'accounting adjustment proposal timestamp is outside the accepted clock window' USING ERRCODE='22008';
  END IF;
  SELECT EXISTS(
    SELECT 1 FROM identity.tenants t
    JOIN identity.memberships m ON m.tenant_id=t.id
    JOIN identity.sessions s ON s.id=NEW.initiator_session_id
    WHERE t.id=NEW.tenant_id AND t.kind='enterprise' AND t.status='active'
      AND m.id=NEW.initiator_membership_id AND m.user_id=NEW.initiator_user_id
      AND m.status='active' AND m.role IN ('owner','contract_admin')
      AND s.user_id=NEW.initiator_user_id AND s.active_tenant_id=NEW.tenant_id
      AND s.revoked_at IS NULL AND s.expires_at>NEW.created_at AND s.reauthenticated_at=NEW.reauthenticated_at
  ) INTO valid_principal;
  IF NOT valid_principal THEN RAISE EXCEPTION 'accounting adjustment proposer is not currently authorized' USING ERRCODE='42501'; END IF;
  SELECT version,granted_units,reserved_units,settled_units INTO bucket_version,granted,reserved,settled
    FROM contracts.credit_buckets WHERE tenant_id=NEW.tenant_id AND id=NEW.bucket_id FOR SHARE;
  IF NOT FOUND OR bucket_version<>NEW.target_bucket_version THEN
    RAISE EXCEPTION 'accounting adjustment target version changed' USING ERRCODE='40001';
  END IF;
  IF granted+NEW.units<=0 OR granted+NEW.units<reserved+settled THEN
    RAISE EXCEPTION 'accounting adjustment would violate the bucket hard cap' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER accounting_adjustment_proposals_validate BEFORE INSERT ON contracts.accounting_adjustment_proposals
  FOR EACH ROW EXECUTE FUNCTION contracts.validate_accounting_adjustment_proposal();

CREATE FUNCTION contracts.validate_accounting_adjustment_approval() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,contracts,identity SET row_security=off AS $$
DECLARE proposal contracts.accounting_adjustment_proposals%ROWTYPE; valid_principal boolean;
BEGIN
  SELECT * INTO proposal FROM contracts.accounting_adjustment_proposals
    WHERE tenant_id=NEW.tenant_id AND id=NEW.proposal_id FOR UPDATE;
  IF NOT FOUND OR proposal.status<>'proposed' OR proposal.expires_at<=NEW.decided_at
    OR NEW.decided_at<proposal.created_at
    OR NEW.decided_at<statement_timestamp()-interval '1 minute' OR NEW.decided_at>statement_timestamp()+interval '1 minute'
    OR proposal.initiator_user_id=NEW.approver_user_id OR proposal.proposal_hash<>NEW.proposal_hash
    OR proposal.target_bucket_version<>NEW.target_bucket_version THEN
    RAISE EXCEPTION 'accounting adjustment approval scope changed' USING ERRCODE='40001';
  END IF;
  IF NEW.decision='approve' AND (SELECT count(*) FROM contracts.accounting_adjustment_approval_decisions d
      WHERE d.tenant_id=NEW.tenant_id AND d.proposal_id=NEW.proposal_id AND d.decision='approve')>=2 THEN
    RAISE EXCEPTION 'accounting adjustment already has two approvals' USING ERRCODE='23505';
  END IF;
  SELECT EXISTS(
    SELECT 1 FROM identity.memberships m JOIN identity.sessions s ON s.id=NEW.session_id
    WHERE m.tenant_id=NEW.tenant_id AND m.id=NEW.membership_id AND m.user_id=NEW.approver_user_id
      AND m.status='active' AND m.role IN ('owner','contract_admin')
      AND s.user_id=NEW.approver_user_id AND s.active_tenant_id=NEW.tenant_id
      AND s.revoked_at IS NULL AND s.expires_at>NEW.decided_at AND s.reauthenticated_at=NEW.reauthenticated_at
  ) INTO valid_principal;
  IF NOT valid_principal THEN RAISE EXCEPTION 'accounting adjustment approver is not currently authorized' USING ERRCODE='42501'; END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER accounting_adjustment_approvals_validate BEFORE INSERT ON contracts.accounting_adjustment_approval_decisions
  FOR EACH ROW EXECUTE FUNCTION contracts.validate_accounting_adjustment_approval();

CREATE FUNCTION contracts.finalize_accounting_adjustment_rejection() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,contracts SET row_security=off AS $$
BEGIN
  IF NEW.decision='reject' THEN
    UPDATE contracts.accounting_adjustment_proposals SET version=2,status='rejected',updated_at=NEW.decided_at
      WHERE tenant_id=NEW.tenant_id AND id=NEW.proposal_id AND version=1 AND status='proposed';
    IF NOT FOUND THEN RAISE EXCEPTION 'accounting adjustment rejection lost its version fence' USING ERRCODE='40001'; END IF;
  END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER accounting_adjustment_approvals_reject AFTER INSERT ON contracts.accounting_adjustment_approval_decisions
  FOR EACH ROW EXECUTE FUNCTION contracts.finalize_accounting_adjustment_rejection();

CREATE OR REPLACE FUNCTION contracts.enforce_credit_bucket_accounting() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,contracts SET row_security=off AS $$
DECLARE reserved_delta bigint; settled_delta bigint; adjustment_id uuid; authorized boolean;
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'credit bucket deletion is forbidden'; END IF;
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.contract_id IS DISTINCT FROM OLD.contract_id OR NEW.bucket_kind IS DISTINCT FROM OLD.bucket_kind
    OR NEW.starts_at IS DISTINCT FROM OLD.starts_at OR NEW.expires_at IS DISTINCT FROM OLD.expires_at
    OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'immutable credit bucket scope mutation is forbidden';
  END IF;
  IF NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at THEN
    RAISE EXCEPTION 'credit bucket version must advance exactly once';
  END IF;
  reserved_delta:=NEW.reserved_units-OLD.reserved_units;
  settled_delta:=NEW.settled_units-OLD.settled_units;
  IF NEW.granted_units IS DISTINCT FROM OLD.granted_units THEN
    adjustment_id:=NULLIF(current_setting('lites.accounting_adjustment_proposal_id',true),'')::uuid;
    SELECT EXISTS(SELECT 1 FROM contracts.accounting_adjustment_proposals p
      WHERE p.id=adjustment_id AND p.tenant_id=OLD.tenant_id AND p.bucket_id=OLD.id
        AND p.target_bucket_version=OLD.version AND p.units=NEW.granted_units-OLD.granted_units
        AND p.status='proposed') INTO authorized;
    IF NOT authorized OR reserved_delta<>0 OR settled_delta<>0 THEN
      RAISE EXCEPTION 'credit grant mutation requires an exact approved adjustment' USING ERRCODE='42501';
    END IF;
    RETURN NEW;
  END IF;
  IF reserved_delta>0 AND settled_delta=0 THEN RETURN NEW; END IF;
  IF reserved_delta<0 AND settled_delta=0 THEN RETURN NEW; END IF;
  IF reserved_delta<0 AND settled_delta>=0 AND settled_delta<=-reserved_delta THEN RETURN NEW; END IF;
  RAISE EXCEPTION 'invalid credit bucket accounting transition';
END
$$;

ALTER TABLE contracts.manual_adjustments
  ADD COLUMN reason_ref text,
  ADD COLUMN reason_hash text,
  ADD COLUMN before_bucket_version bigint,
  ADD COLUMN after_bucket_version bigint,
  ADD COLUMN before_granted_units bigint,
  ADD COLUMN after_granted_units bigint,
  ADD COLUMN approval_event_ids jsonb;
ALTER TABLE contracts.manual_adjustments
  ALTER COLUMN reason_ref SET NOT NULL,
  ALTER COLUMN reason_hash SET NOT NULL,
  ALTER COLUMN before_bucket_version SET NOT NULL,
  ALTER COLUMN after_bucket_version SET NOT NULL,
  ALTER COLUMN before_granted_units SET NOT NULL,
  ALTER COLUMN after_granted_units SET NOT NULL,
  ALTER COLUMN approval_event_ids SET NOT NULL,
  ADD CONSTRAINT manual_adjustments_bucket_exact_fk FOREIGN KEY (tenant_id,bucket_id)
    REFERENCES contracts.credit_buckets(tenant_id,id) ON DELETE RESTRICT,
  ADD CONSTRAINT manual_adjustments_completeness CHECK (
    units<>0 AND length(btrim(reason))>0 AND length(btrim(reason_ref))>0
    AND reason_hash ~ '^[0-9a-f]{64}$' AND proposal_hash ~ '^[0-9a-f]{64}$'
    AND after_bucket_version=before_bucket_version+1
    AND after_granted_units=before_granted_units+units AND after_granted_units>0
    AND jsonb_typeof(approval_event_ids)='array' AND jsonb_array_length(approval_event_ids)=2
  );

CREATE FUNCTION contracts.apply_approved_accounting_adjustment(
  p_tenant_id uuid,p_proposal_id uuid,p_actor_user_id uuid,p_adjustment_id uuid,p_now timestamptz
) RETURNS TABLE(bucket_id uuid,bucket_version bigint,granted_units bigint)
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,contracts,identity SET row_security=off AS $$
DECLARE proposal contracts.accounting_adjustment_proposals%ROWTYPE; bucket contracts.credit_buckets%ROWTYPE;
  approvals integer; approval_events jsonb;
BEGIN
  IF NULLIF(current_setting('lites.tenant_id',true),'')::uuid IS DISTINCT FROM p_tenant_id
    OR p_proposal_id IS NULL OR p_actor_user_id IS NULL OR p_adjustment_id IS NULL OR p_now IS NULL THEN
    RAISE EXCEPTION 'invalid accounting adjustment arguments' USING ERRCODE='42501';
  END IF;
  IF p_now<statement_timestamp()-interval '1 minute' OR p_now>statement_timestamp()+interval '1 minute' THEN
    RAISE EXCEPTION 'accounting adjustment timestamp is outside the accepted clock window' USING ERRCODE='22008';
  END IF;
  SELECT * INTO proposal FROM contracts.accounting_adjustment_proposals
    WHERE tenant_id=p_tenant_id AND id=p_proposal_id FOR UPDATE;
  IF NOT FOUND OR proposal.status<>'proposed' OR proposal.created_at>p_now OR proposal.expires_at<=p_now THEN
    RAISE EXCEPTION 'accounting adjustment proposal is not executable' USING ERRCODE='40001';
  END IF;
  IF NOT EXISTS(SELECT 1 FROM identity.memberships m JOIN identity.sessions s ON s.id=proposal.initiator_session_id
    WHERE m.tenant_id=p_tenant_id AND m.id=proposal.initiator_membership_id AND m.user_id=proposal.initiator_user_id
      AND m.status='active' AND m.role IN ('owner','contract_admin')
      AND s.user_id=proposal.initiator_user_id AND s.active_tenant_id=p_tenant_id
      AND s.revoked_at IS NULL AND s.expires_at>p_now AND s.reauthenticated_at=proposal.reauthenticated_at)
  THEN RAISE EXCEPTION 'accounting adjustment proposer authorization changed' USING ERRCODE='42501'; END IF;
  SELECT count(*),jsonb_agg(d.event_id ORDER BY d.decided_at,d.id) INTO approvals,approval_events
    FROM contracts.accounting_adjustment_approval_decisions d
    JOIN identity.memberships m ON m.tenant_id=d.tenant_id AND m.id=d.membership_id
      AND m.user_id=d.approver_user_id AND m.status='active' AND m.role IN ('owner','contract_admin')
    JOIN identity.sessions s ON s.id=d.session_id AND s.user_id=d.approver_user_id
      AND s.active_tenant_id=d.tenant_id AND s.revoked_at IS NULL AND s.expires_at>p_now
      AND s.reauthenticated_at=d.reauthenticated_at
    WHERE d.tenant_id=p_tenant_id AND d.proposal_id=p_proposal_id AND d.decision='approve'
      AND d.proposal_hash=proposal.proposal_hash AND d.target_bucket_version=proposal.target_bucket_version
      AND d.decided_at<=p_now;
  IF approvals<>2 OR NOT EXISTS(SELECT 1 FROM contracts.accounting_adjustment_approval_decisions d
      WHERE d.tenant_id=p_tenant_id AND d.proposal_id=p_proposal_id AND d.approver_user_id=p_actor_user_id AND d.decision='approve') THEN
    RAISE EXCEPTION 'two current accounting adjustment approvals are required' USING ERRCODE='42501';
  END IF;
  SELECT * INTO bucket FROM contracts.credit_buckets
    WHERE tenant_id=p_tenant_id AND id=proposal.bucket_id FOR UPDATE;
  IF NOT FOUND OR bucket.version<>proposal.target_bucket_version THEN
    RAISE EXCEPTION 'accounting adjustment target version changed' USING ERRCODE='40001';
  END IF;
  IF bucket.granted_units+proposal.units<=0 OR bucket.granted_units+proposal.units<bucket.reserved_units+bucket.settled_units THEN
    RAISE EXCEPTION 'accounting adjustment would violate the bucket hard cap' USING ERRCODE='23514';
  END IF;
  PERFORM set_config('lites.accounting_adjustment_proposal_id',p_proposal_id::text,true);
  UPDATE contracts.credit_buckets AS cb SET version=cb.version+1,granted_units=cb.granted_units+proposal.units,updated_at=p_now
    WHERE cb.tenant_id=p_tenant_id AND cb.id=proposal.bucket_id RETURNING cb.* INTO bucket;
  UPDATE contracts.accounting_adjustment_proposals SET version=2,status='executed',executed_at=p_now,updated_at=p_now
    WHERE tenant_id=p_tenant_id AND id=p_proposal_id AND version=1 AND status='proposed';
  INSERT INTO contracts.manual_adjustments(id,tenant_id,bucket_id,units,reason,reason_ref,reason_hash,proposal_hash,
      approved_event_id,approval_event_ids,actor_user_id,before_bucket_version,after_bucket_version,
      before_granted_units,after_granted_units,recorded_at,created_at,updated_at)
    VALUES(p_adjustment_id,p_tenant_id,bucket.id,proposal.units,proposal.reason_hash,proposal.reason_ref,proposal.reason_hash,
      proposal.proposal_hash,(approval_events->>0)::uuid,approval_events,p_actor_user_id,proposal.target_bucket_version,
      bucket.version,bucket.granted_units-proposal.units,bucket.granted_units,p_now,p_now,p_now);
  RETURN QUERY SELECT bucket.id,bucket.version,bucket.granted_units;
END
$$;
REVOKE ALL ON FUNCTION contracts.apply_approved_accounting_adjustment(uuid,uuid,uuid,uuid,timestamptz) FROM PUBLIC;

CREATE INDEX accounting_adjustment_proposals_status_idx
  ON contracts.accounting_adjustment_proposals(tenant_id,status,expires_at,id);
CREATE INDEX accounting_adjustment_approvals_proposal_idx
  ON contracts.accounting_adjustment_approval_decisions(tenant_id,proposal_id,decided_at,id);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_contract_service') THEN
    GRANT SELECT ON contracts.credit_buckets,contracts.manual_adjustments TO lites_contract_service;
    GRANT SELECT,INSERT ON contracts.accounting_adjustment_proposals,contracts.accounting_adjustment_approval_decisions TO lites_contract_service;
    GRANT SELECT,INSERT,UPDATE ON agent.event_cursors TO lites_contract_service;
    GRANT EXECUTE ON FUNCTION contracts.apply_approved_accounting_adjustment(uuid,uuid,uuid,uuid,timestamptz) TO lites_contract_service;
  END IF;
END
$$;
