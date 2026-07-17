BEGIN;

ALTER TABLE contracts.contract_entitlements
  ADD CONSTRAINT contract_entitlements_tenant_id_id_unique UNIQUE (tenant_id,id),
  ADD CONSTRAINT contract_entitlements_contract_exact_fk FOREIGN KEY (tenant_id,contract_id)
    REFERENCES contracts.contracts(tenant_id,id) ON DELETE RESTRICT,
  ADD CONSTRAINT contract_entitlements_scope CHECK (
    entitlement_key IN ('programs','cohorts','role_packs','aggregate_analytics','audit_export','private_delivery','commercial_license','support_tier')
    AND (limit_value IS NULL OR limit_value>=0)
    AND jsonb_typeof(config)='object' AND octet_length(config::text)<=16384
    AND effective_at<COALESCE(expires_at,'infinity'::timestamptz)
  );

CREATE TABLE contracts.entitlement_proposals (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version>0),
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  initiator_user_id uuid NOT NULL,
  initiator_membership_id uuid NOT NULL,
  initiator_session_id uuid NOT NULL,
  reauthenticated_at timestamptz NOT NULL,
  contract_id uuid NOT NULL,
  target_contract_version bigint NOT NULL,
  entitlement_key text NOT NULL,
  target_entitlement_version bigint NOT NULL,
  limit_value bigint,
  config jsonb NOT NULL,
  status text NOT NULL DEFAULT 'proposed',
  proposal_hash text NOT NULL,
  reason_ref text NOT NULL,
  reason_hash text NOT NULL,
  expires_at timestamptz NOT NULL,
  executed_at timestamptz,
  UNIQUE (tenant_id,id),
  UNIQUE (tenant_id,proposal_hash),
  CONSTRAINT entitlement_proposals_tenant_fk FOREIGN KEY (tenant_id) REFERENCES identity.tenants(id) ON DELETE RESTRICT,
  CONSTRAINT entitlement_proposals_contract_fk FOREIGN KEY (tenant_id,contract_id) REFERENCES contracts.contracts(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT entitlement_proposals_membership_fk FOREIGN KEY (tenant_id,initiator_membership_id) REFERENCES identity.memberships(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT entitlement_proposals_session_fk FOREIGN KEY (initiator_session_id) REFERENCES identity.sessions(id) ON DELETE RESTRICT,
  CONSTRAINT entitlement_proposals_contract CHECK (
    status IN ('proposed','rejected','expired','executed')
    AND target_contract_version>0 AND target_entitlement_version>=0
    AND entitlement_key IN ('programs','cohorts','role_packs','aggregate_analytics','audit_export','private_delivery','commercial_license','support_tier')
    AND (limit_value IS NULL OR limit_value>=0)
    AND jsonb_typeof(config)='object' AND octet_length(config::text)<=16384
    AND proposal_hash ~ '^[0-9a-f]{64}$' AND reason_hash ~ '^[0-9a-f]{64}$'
    AND length(btrim(reason_ref)) BETWEEN 1 AND 2048
    AND expires_at>created_at
    AND reauthenticated_at<=created_at AND reauthenticated_at>=created_at-interval '5 minutes'
    AND ((status='proposed' AND version=1 AND executed_at IS NULL)
      OR (status IN ('rejected','expired') AND version=2 AND executed_at IS NULL)
      OR (status='executed' AND version=2 AND executed_at IS NOT NULL))
  )
);
ALTER TABLE contracts.entitlement_proposals ENABLE ROW LEVEL SECURITY;
ALTER TABLE contracts.entitlement_proposals FORCE ROW LEVEL SECURITY;
CREATE POLICY entitlement_proposals_tenant_isolation ON contracts.entitlement_proposals
  USING (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid);

CREATE TABLE contracts.entitlement_approval_decisions (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  proposal_id uuid NOT NULL,
  approver_user_id uuid NOT NULL,
  membership_id uuid NOT NULL,
  session_id uuid NOT NULL,
  decision text NOT NULL,
  proposal_hash text NOT NULL,
  target_contract_version bigint NOT NULL,
  target_entitlement_version bigint NOT NULL,
  permission_snapshot text NOT NULL,
  reauthenticated_at timestamptz NOT NULL,
  decided_at timestamptz NOT NULL,
  event_id uuid NOT NULL,
  created_at timestamptz NOT NULL,
  UNIQUE (tenant_id,proposal_id,approver_user_id),
  UNIQUE (tenant_id,proposal_id,session_id),
  UNIQUE (tenant_id,event_id),
  CONSTRAINT entitlement_approval_proposal_fk FOREIGN KEY (tenant_id,proposal_id) REFERENCES contracts.entitlement_proposals(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT entitlement_approval_membership_fk FOREIGN KEY (tenant_id,membership_id) REFERENCES identity.memberships(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT entitlement_approval_event_fk FOREIGN KEY (event_id) REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  CONSTRAINT entitlement_approval_contract CHECK (
    decision IN ('approve','reject') AND proposal_hash ~ '^[0-9a-f]{64}$'
    AND target_contract_version>0 AND target_entitlement_version>=0
    AND permission_snapshot ~ '^[0-9a-f]{64}$'
    AND reauthenticated_at<=decided_at AND reauthenticated_at>=decided_at-interval '5 minutes'
  )
);
ALTER TABLE contracts.entitlement_approval_decisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE contracts.entitlement_approval_decisions FORCE ROW LEVEL SECURITY;
CREATE POLICY entitlement_approval_decisions_tenant_isolation ON contracts.entitlement_approval_decisions
  USING (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid);
CREATE TRIGGER entitlement_approval_decisions_append_only BEFORE UPDATE OR DELETE ON contracts.entitlement_approval_decisions
  FOR EACH ROW EXECUTE FUNCTION agent.reject_append_only_mutation();

CREATE FUNCTION contracts.enforce_entitlement_proposal_lifecycle() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog,contracts AS $$
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'entitlement proposal deletion is forbidden'; END IF;
  IF OLD.status<>'proposed' OR OLD.version<>1
    OR NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.initiator_user_id IS DISTINCT FROM OLD.initiator_user_id OR NEW.initiator_membership_id IS DISTINCT FROM OLD.initiator_membership_id
    OR NEW.initiator_session_id IS DISTINCT FROM OLD.initiator_session_id OR NEW.reauthenticated_at IS DISTINCT FROM OLD.reauthenticated_at
    OR NEW.contract_id IS DISTINCT FROM OLD.contract_id OR NEW.target_contract_version IS DISTINCT FROM OLD.target_contract_version
    OR NEW.entitlement_key IS DISTINCT FROM OLD.entitlement_key OR NEW.target_entitlement_version IS DISTINCT FROM OLD.target_entitlement_version
    OR NEW.limit_value IS DISTINCT FROM OLD.limit_value OR NEW.config IS DISTINCT FROM OLD.config
    OR NEW.proposal_hash IS DISTINCT FROM OLD.proposal_hash OR NEW.reason_ref IS DISTINCT FROM OLD.reason_ref OR NEW.reason_hash IS DISTINCT FROM OLD.reason_hash
    OR NEW.expires_at IS DISTINCT FROM OLD.expires_at OR NEW.created_at IS DISTINCT FROM OLD.created_at
    OR NEW.version<>2 OR NEW.status NOT IN ('rejected','expired','executed') OR NEW.updated_at<OLD.updated_at
    OR (NEW.status='executed')<>(NEW.executed_at IS NOT NULL)
  THEN RAISE EXCEPTION 'invalid entitlement proposal transition'; END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER entitlement_proposals_lifecycle BEFORE UPDATE OR DELETE ON contracts.entitlement_proposals
  FOR EACH ROW EXECUTE FUNCTION contracts.enforce_entitlement_proposal_lifecycle();

CREATE FUNCTION contracts.validate_entitlement_proposal() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,contracts,identity SET row_security=off AS $$
DECLARE valid_principal boolean; contract_version bigint; contract_status text; entitlement_version bigint;
BEGIN
  IF NEW.created_at<statement_timestamp()-interval '1 minute' OR NEW.created_at>statement_timestamp()+interval '1 minute' THEN
    RAISE EXCEPTION 'entitlement proposal timestamp is outside the accepted clock window' USING ERRCODE='22008';
  END IF;
  SELECT EXISTS(SELECT 1 FROM identity.tenants t
    JOIN identity.memberships m ON m.tenant_id=t.id
    JOIN identity.sessions s ON s.id=NEW.initiator_session_id
    WHERE t.id=NEW.tenant_id AND t.kind='enterprise' AND t.status='active'
      AND m.id=NEW.initiator_membership_id AND m.user_id=NEW.initiator_user_id AND m.status='active' AND m.role IN ('owner','contract_admin')
      AND s.user_id=NEW.initiator_user_id AND s.active_tenant_id=NEW.tenant_id AND s.revoked_at IS NULL AND s.expires_at>NEW.created_at
      AND s.reauthenticated_at=NEW.reauthenticated_at) INTO valid_principal;
  IF NOT valid_principal THEN RAISE EXCEPTION 'entitlement proposer is not currently authorized' USING ERRCODE='42501'; END IF;
  SELECT version,status INTO contract_version,contract_status FROM contracts.contracts
    WHERE tenant_id=NEW.tenant_id AND id=NEW.contract_id FOR SHARE;
  IF NOT FOUND OR contract_version<>NEW.target_contract_version OR contract_status='terminated' THEN
    RAISE EXCEPTION 'entitlement contract version changed' USING ERRCODE='40001';
  END IF;
  SELECT COALESCE(max(version),0) INTO entitlement_version FROM contracts.contract_entitlements
    WHERE tenant_id=NEW.tenant_id AND contract_id=NEW.contract_id AND entitlement_key=NEW.entitlement_key;
  IF entitlement_version<>NEW.target_entitlement_version THEN RAISE EXCEPTION 'entitlement target version changed' USING ERRCODE='40001'; END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER entitlement_proposals_validate BEFORE INSERT ON contracts.entitlement_proposals
  FOR EACH ROW EXECUTE FUNCTION contracts.validate_entitlement_proposal();

CREATE FUNCTION contracts.validate_entitlement_approval() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,contracts,identity SET row_security=off AS $$
DECLARE proposal contracts.entitlement_proposals%ROWTYPE; valid_principal boolean; decisions integer;
BEGIN
  IF NEW.decided_at<statement_timestamp()-interval '1 minute' OR NEW.decided_at>statement_timestamp()+interval '1 minute' THEN
    RAISE EXCEPTION 'entitlement approval timestamp is outside the accepted clock window' USING ERRCODE='22008';
  END IF;
  SELECT * INTO proposal FROM contracts.entitlement_proposals WHERE tenant_id=NEW.tenant_id AND id=NEW.proposal_id FOR UPDATE;
  IF NOT FOUND OR proposal.status<>'proposed' OR proposal.version<>1 OR proposal.expires_at<=NEW.decided_at
    OR proposal.proposal_hash<>NEW.proposal_hash OR proposal.target_contract_version<>NEW.target_contract_version
    OR proposal.target_entitlement_version<>NEW.target_entitlement_version OR proposal.initiator_user_id=NEW.approver_user_id THEN
    RAISE EXCEPTION 'entitlement approval scope is invalid' USING ERRCODE='40001';
  END IF;
  SELECT EXISTS(SELECT 1 FROM identity.memberships m JOIN identity.sessions s ON s.id=NEW.session_id
    WHERE m.tenant_id=NEW.tenant_id AND m.id=NEW.membership_id AND m.user_id=NEW.approver_user_id
      AND m.status='active' AND m.role IN ('owner','contract_admin')
      AND s.user_id=NEW.approver_user_id AND s.active_tenant_id=NEW.tenant_id AND s.revoked_at IS NULL AND s.expires_at>NEW.decided_at
      AND s.reauthenticated_at=NEW.reauthenticated_at) INTO valid_principal;
  IF NOT valid_principal THEN RAISE EXCEPTION 'entitlement approver is not currently authorized' USING ERRCODE='42501'; END IF;
  SELECT count(*) INTO decisions FROM contracts.entitlement_approval_decisions WHERE tenant_id=NEW.tenant_id AND proposal_id=NEW.proposal_id;
  IF decisions>=2 THEN RAISE EXCEPTION 'entitlement proposal already has two decisions' USING ERRCODE='23505'; END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER entitlement_approvals_validate BEFORE INSERT ON contracts.entitlement_approval_decisions
  FOR EACH ROW EXECUTE FUNCTION contracts.validate_entitlement_approval();

CREATE FUNCTION contracts.finalize_entitlement_rejection() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,contracts SET row_security=off AS $$
BEGIN
  IF NEW.decision='reject' THEN
    UPDATE contracts.entitlement_proposals SET version=2,status='rejected',updated_at=NEW.decided_at
      WHERE tenant_id=NEW.tenant_id AND id=NEW.proposal_id AND version=1 AND status='proposed';
    IF NOT FOUND THEN RAISE EXCEPTION 'entitlement proposal rejection lost its version fence' USING ERRCODE='40001'; END IF;
  END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER entitlement_approvals_reject AFTER INSERT ON contracts.entitlement_approval_decisions
  FOR EACH ROW EXECUTE FUNCTION contracts.finalize_entitlement_rejection();

CREATE FUNCTION contracts.enforce_entitlement_append() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,contracts SET row_security=off AS $$
DECLARE proposal_id uuid; authorized boolean;
BEGIN
  IF TG_OP<>'INSERT' THEN RAISE EXCEPTION 'contract entitlement mutation is append-only'; END IF;
  proposal_id:=NULLIF(current_setting('lites.entitlement_proposal_id',true),'')::uuid;
  SELECT EXISTS(SELECT 1 FROM contracts.entitlement_proposals p
    WHERE p.id=proposal_id AND p.tenant_id=NEW.tenant_id AND p.contract_id=NEW.contract_id
      AND p.entitlement_key=NEW.entitlement_key AND NEW.version=p.target_entitlement_version+1
      AND NEW.limit_value IS NOT DISTINCT FROM p.limit_value AND NEW.config=p.config AND p.status='proposed') INTO authorized;
  IF NOT authorized THEN RAISE EXCEPTION 'entitlement append requires exact approved proposal' USING ERRCODE='42501'; END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER contract_entitlements_append BEFORE INSERT OR UPDATE OR DELETE ON contracts.contract_entitlements
  FOR EACH ROW EXECUTE FUNCTION contracts.enforce_entitlement_append();

CREATE FUNCTION contracts.apply_approved_entitlement_proposal(
  p_tenant_id uuid,p_proposal_id uuid,p_actor_user_id uuid,p_entitlement_id uuid,p_audit_id uuid,p_now timestamptz
) RETURNS TABLE(entitlement_id uuid,entitlement_version bigint)
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,contracts,identity SET row_security=off AS $$
DECLARE proposal contracts.entitlement_proposals%ROWTYPE; current_contract contracts.contracts%ROWTYPE;
  approvals integer; approval_events jsonb; current_version bigint;
BEGIN
  IF NULLIF(current_setting('lites.tenant_id',true),'')::uuid IS DISTINCT FROM p_tenant_id
    OR p_proposal_id IS NULL OR p_actor_user_id IS NULL OR p_entitlement_id IS NULL OR p_audit_id IS NULL OR p_now IS NULL THEN
    RAISE EXCEPTION 'invalid entitlement execution arguments' USING ERRCODE='42501';
  END IF;
  IF p_now<statement_timestamp()-interval '1 minute' OR p_now>statement_timestamp()+interval '1 minute' THEN
    RAISE EXCEPTION 'entitlement execution timestamp is outside the accepted clock window' USING ERRCODE='22008';
  END IF;
  SELECT * INTO proposal FROM contracts.entitlement_proposals WHERE tenant_id=p_tenant_id AND id=p_proposal_id FOR UPDATE;
  IF NOT FOUND OR proposal.status<>'proposed' OR proposal.created_at>p_now OR proposal.expires_at<=p_now THEN
    RAISE EXCEPTION 'entitlement proposal is not executable' USING ERRCODE='40001';
  END IF;
  IF NOT EXISTS(SELECT 1 FROM identity.memberships m JOIN identity.sessions s ON s.id=proposal.initiator_session_id
    WHERE m.tenant_id=p_tenant_id AND m.id=proposal.initiator_membership_id AND m.user_id=proposal.initiator_user_id
      AND m.status='active' AND m.role IN ('owner','contract_admin')
      AND s.user_id=proposal.initiator_user_id AND s.active_tenant_id=p_tenant_id AND s.revoked_at IS NULL AND s.expires_at>p_now
      AND s.reauthenticated_at=proposal.reauthenticated_at) THEN
    RAISE EXCEPTION 'entitlement proposer authorization changed' USING ERRCODE='42501';
  END IF;
  SELECT count(*),jsonb_agg(d.event_id ORDER BY d.decided_at,d.id) INTO approvals,approval_events
    FROM contracts.entitlement_approval_decisions d
    JOIN identity.memberships m ON m.tenant_id=d.tenant_id AND m.id=d.membership_id AND m.user_id=d.approver_user_id AND m.status='active' AND m.role IN ('owner','contract_admin')
    JOIN identity.sessions s ON s.id=d.session_id AND s.user_id=d.approver_user_id AND s.active_tenant_id=d.tenant_id AND s.revoked_at IS NULL AND s.expires_at>p_now AND s.reauthenticated_at=d.reauthenticated_at
    WHERE d.tenant_id=p_tenant_id AND d.proposal_id=p_proposal_id AND d.decision='approve'
      AND d.proposal_hash=proposal.proposal_hash AND d.target_contract_version=proposal.target_contract_version
      AND d.target_entitlement_version=proposal.target_entitlement_version AND d.decided_at<=p_now;
  IF approvals<>2 OR NOT EXISTS(SELECT 1 FROM contracts.entitlement_approval_decisions d WHERE d.tenant_id=p_tenant_id AND d.proposal_id=p_proposal_id AND d.approver_user_id=p_actor_user_id AND d.decision='approve') THEN
    RAISE EXCEPTION 'two current entitlement approvals are required' USING ERRCODE='42501';
  END IF;
  SELECT * INTO current_contract FROM contracts.contracts WHERE tenant_id=p_tenant_id AND id=proposal.contract_id FOR UPDATE;
  IF NOT FOUND OR current_contract.version<>proposal.target_contract_version OR current_contract.status='terminated' THEN
    RAISE EXCEPTION 'entitlement contract version changed' USING ERRCODE='40001';
  END IF;
  PERFORM 1 FROM contracts.contract_entitlements WHERE tenant_id=p_tenant_id AND contract_id=proposal.contract_id AND entitlement_key=proposal.entitlement_key FOR UPDATE;
  SELECT COALESCE(max(version),0) INTO current_version FROM contracts.contract_entitlements WHERE tenant_id=p_tenant_id AND contract_id=proposal.contract_id AND entitlement_key=proposal.entitlement_key;
  IF current_version<>proposal.target_entitlement_version THEN RAISE EXCEPTION 'entitlement target version changed' USING ERRCODE='40001'; END IF;
  PERFORM set_config('lites.entitlement_proposal_id',p_proposal_id::text,true);
  INSERT INTO contracts.contract_entitlements(id,tenant_id,contract_id,version,entitlement_key,limit_value,config,effective_at,expires_at,created_at,updated_at)
    VALUES(p_entitlement_id,p_tenant_id,proposal.contract_id,current_version+1,proposal.entitlement_key,proposal.limit_value,proposal.config,p_now,current_contract.ends_at,p_now,p_now);
  UPDATE contracts.entitlement_proposals SET version=2,status='executed',executed_at=p_now,updated_at=p_now WHERE tenant_id=p_tenant_id AND id=p_proposal_id AND version=1 AND status='proposed';
  INSERT INTO contracts.contract_audit_events(id,tenant_id,contract_id,actor_user_id,event_type,reason,before_version,after_version,proposal_hash,approval_event_ids,occurred_at)
    VALUES(p_audit_id,p_tenant_id,proposal.contract_id,p_actor_user_id,'entitlement_set_executed',proposal.reason_hash,
      CASE WHEN current_version=0 THEN NULL ELSE current_version END,current_version+1,proposal.proposal_hash,approval_events,p_now);
  RETURN QUERY SELECT p_entitlement_id,current_version+1;
END
$$;
REVOKE ALL ON FUNCTION contracts.apply_approved_entitlement_proposal(uuid,uuid,uuid,uuid,uuid,timestamptz) FROM PUBLIC;

CREATE INDEX entitlement_proposals_status_idx ON contracts.entitlement_proposals(tenant_id,status,expires_at,id);
CREATE INDEX entitlement_approvals_proposal_idx ON contracts.entitlement_approval_decisions(tenant_id,proposal_id,decided_at,id);
CREATE INDEX contract_entitlements_current_idx ON contracts.contract_entitlements(tenant_id,contract_id,entitlement_key,version DESC);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_contract_service') THEN
    GRANT SELECT ON contracts.contract_entitlements TO lites_contract_service;
    GRANT SELECT,INSERT ON contracts.entitlement_proposals,contracts.entitlement_approval_decisions TO lites_contract_service;
    GRANT EXECUTE ON FUNCTION contracts.apply_approved_entitlement_proposal(uuid,uuid,uuid,uuid,uuid,timestamptz) TO lites_contract_service;
  END IF;
END
$$;

COMMIT;
