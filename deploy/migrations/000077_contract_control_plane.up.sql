DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM contracts.contracts)
    OR EXISTS (SELECT 1 FROM contracts.seat_allocations)
    OR EXISTS (SELECT 1 FROM contracts.contract_audit_events) THEN
    RAISE EXCEPTION 'contract control-plane backfill required before migration 77' USING ERRCODE='55000';
  END IF;
END
$$;

ALTER TABLE identity.memberships
  ADD CONSTRAINT memberships_tenant_id_id_unique UNIQUE (tenant_id,id);

ALTER TABLE contracts.contracts
  ADD CONSTRAINT contracts_tenant_id_id_unique UNIQUE (tenant_id,id),
  ADD CONSTRAINT contracts_control_plane_contract CHECK (
    length(btrim(contract_number)) BETWEEN 1 AND 200
    AND status IN ('active','suspended','terminated')
    AND starts_at<ends_at AND seat_limit>0
    AND length(btrim(region)) BETWEEN 1 AND 100
    AND license_kind IN ('enterprise_cloud','private_cloud','commercial_self_hosted')
    AND proposal_hash ~ '^[0-9a-f]{64}$'
    AND activated_at IS NOT NULL
  );
CREATE UNIQUE INDEX contracts_one_live_per_tenant_idx ON contracts.contracts(tenant_id)
  WHERE status IN ('active','suspended');

CREATE TABLE contracts.contract_proposals (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version>0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  initiator_user_id uuid NOT NULL,
  initiator_membership_id uuid NOT NULL,
  initiator_session_id uuid NOT NULL,
  reauthenticated_at timestamptz NOT NULL,
  action text NOT NULL,
  target_contract_id uuid NOT NULL,
  target_version bigint NOT NULL,
  status text NOT NULL DEFAULT 'proposed',
  proposal_hash text NOT NULL,
  reason_ref text NOT NULL,
  reason_hash text NOT NULL,
  contract_number text,
  starts_at timestamptz,
  ends_at timestamptz,
  seat_limit integer,
  region text,
  license_kind text,
  expires_at timestamptz NOT NULL,
  executed_at timestamptz,
  UNIQUE (tenant_id,id),
  UNIQUE (tenant_id,proposal_hash),
  CONSTRAINT contract_proposals_tenant_fk FOREIGN KEY (tenant_id)
    REFERENCES identity.tenants(id) ON DELETE RESTRICT,
  CONSTRAINT contract_proposals_initiator_membership_fk FOREIGN KEY (tenant_id,initiator_membership_id)
    REFERENCES identity.memberships(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT contract_proposals_initiator_session_fk FOREIGN KEY (initiator_session_id)
    REFERENCES identity.sessions(id) ON DELETE RESTRICT,
  CONSTRAINT contract_proposals_contract CHECK (
    action IN ('create','renew','suspend','terminate')
    AND status IN ('proposed','rejected','expired','executed')
    AND proposal_hash ~ '^[0-9a-f]{64}$' AND reason_hash ~ '^[0-9a-f]{64}$'
    AND length(btrim(reason_ref)) BETWEEN 1 AND 1000
    AND expires_at>created_at
    AND reauthenticated_at<=created_at AND reauthenticated_at>=created_at-interval '5 minutes'
    AND ((status='proposed' AND version=1 AND executed_at IS NULL)
      OR (status IN ('rejected','expired') AND version=2 AND executed_at IS NULL)
      OR (status='executed' AND version=2 AND executed_at IS NOT NULL))
    AND ((action='create' AND target_version=0
      AND length(btrim(contract_number)) BETWEEN 1 AND 200
      AND starts_at IS NOT NULL AND ends_at IS NOT NULL AND starts_at<ends_at
      AND seat_limit>0 AND length(btrim(region)) BETWEEN 1 AND 100
      AND license_kind IN ('enterprise_cloud','private_cloud','commercial_self_hosted'))
      OR (action='renew' AND target_version>0
      AND length(btrim(contract_number)) BETWEEN 1 AND 200
      AND starts_at IS NOT NULL AND ends_at IS NOT NULL AND starts_at<ends_at
      AND seat_limit>0 AND length(btrim(region)) BETWEEN 1 AND 100
      AND license_kind IN ('enterprise_cloud','private_cloud','commercial_self_hosted'))
      OR (action IN ('suspend','terminate') AND target_version>0
      AND contract_number IS NULL AND starts_at IS NULL AND ends_at IS NULL
      AND seat_limit IS NULL AND region IS NULL AND license_kind IS NULL))
  )
);
ALTER TABLE contracts.contract_proposals ENABLE ROW LEVEL SECURITY;
ALTER TABLE contracts.contract_proposals FORCE ROW LEVEL SECURITY;
CREATE POLICY contract_proposals_tenant_isolation ON contracts.contract_proposals
  USING (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid);

CREATE TABLE contracts.contract_approval_decisions (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  proposal_id uuid NOT NULL,
  approver_user_id uuid NOT NULL,
  membership_id uuid NOT NULL,
  session_id uuid NOT NULL,
  decision text NOT NULL,
  proposal_hash text NOT NULL,
  target_version bigint NOT NULL,
  permission_snapshot text NOT NULL,
  reauthenticated_at timestamptz NOT NULL,
  decided_at timestamptz NOT NULL,
  event_id uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE (tenant_id,proposal_id,approver_user_id),
  UNIQUE (tenant_id,proposal_id,session_id),
  UNIQUE (tenant_id,event_id),
  CONSTRAINT contract_approval_proposal_fk FOREIGN KEY (tenant_id,proposal_id)
    REFERENCES contracts.contract_proposals(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT contract_approval_membership_fk FOREIGN KEY (tenant_id,membership_id)
    REFERENCES identity.memberships(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT contract_approval_event_fk FOREIGN KEY (event_id)
    REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  CONSTRAINT contract_approval_decision_contract CHECK (
    decision IN ('approve','reject') AND proposal_hash ~ '^[0-9a-f]{64}$'
    AND target_version>=0 AND permission_snapshot ~ '^[0-9a-f]{64}$'
    AND reauthenticated_at<=decided_at AND reauthenticated_at>=decided_at-interval '5 minutes'
  )
);
ALTER TABLE contracts.contract_approval_decisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE contracts.contract_approval_decisions FORCE ROW LEVEL SECURITY;
CREATE POLICY contract_approval_decisions_tenant_isolation ON contracts.contract_approval_decisions
  USING (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid);
CREATE TRIGGER contract_approval_decisions_append_only BEFORE UPDATE OR DELETE
  ON contracts.contract_approval_decisions FOR EACH ROW EXECUTE FUNCTION agent.reject_append_only_mutation();

CREATE FUNCTION contracts.enforce_contract_proposal_lifecycle() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog,contracts AS $$
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'contract proposal deletion is forbidden'; END IF;
  IF OLD.status<>'proposed' OR OLD.version<>1
    OR NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.initiator_user_id IS DISTINCT FROM OLD.initiator_user_id
    OR NEW.initiator_membership_id IS DISTINCT FROM OLD.initiator_membership_id
    OR NEW.initiator_session_id IS DISTINCT FROM OLD.initiator_session_id
    OR NEW.reauthenticated_at IS DISTINCT FROM OLD.reauthenticated_at
    OR NEW.action IS DISTINCT FROM OLD.action OR NEW.target_contract_id IS DISTINCT FROM OLD.target_contract_id
    OR NEW.target_version IS DISTINCT FROM OLD.target_version OR NEW.proposal_hash IS DISTINCT FROM OLD.proposal_hash
    OR NEW.reason_ref IS DISTINCT FROM OLD.reason_ref OR NEW.reason_hash IS DISTINCT FROM OLD.reason_hash
    OR NEW.contract_number IS DISTINCT FROM OLD.contract_number OR NEW.starts_at IS DISTINCT FROM OLD.starts_at
    OR NEW.ends_at IS DISTINCT FROM OLD.ends_at OR NEW.seat_limit IS DISTINCT FROM OLD.seat_limit
    OR NEW.region IS DISTINCT FROM OLD.region OR NEW.license_kind IS DISTINCT FROM OLD.license_kind
    OR NEW.expires_at IS DISTINCT FROM OLD.expires_at OR NEW.created_at IS DISTINCT FROM OLD.created_at
    OR NEW.version<>2 OR NEW.status NOT IN ('rejected','expired','executed') OR NEW.updated_at<OLD.updated_at
    OR (NEW.status='executed')<>(NEW.executed_at IS NOT NULL)
  THEN RAISE EXCEPTION 'invalid contract proposal transition'; END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER contract_proposals_lifecycle BEFORE UPDATE OR DELETE ON contracts.contract_proposals
  FOR EACH ROW EXECUTE FUNCTION contracts.enforce_contract_proposal_lifecycle();

CREATE FUNCTION contracts.validate_contract_proposal() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,contracts,identity SET row_security=off AS $$
DECLARE valid_principal boolean; target contracts.contracts%ROWTYPE;
BEGIN
  IF NEW.created_at<statement_timestamp()-interval '1 minute'
    OR NEW.created_at>statement_timestamp()+interval '1 minute' THEN
    RAISE EXCEPTION 'contract proposal timestamp is outside the accepted clock window' USING ERRCODE='22008';
  END IF;
  SELECT EXISTS(
    SELECT 1 FROM identity.tenants t
    JOIN identity.memberships m ON m.tenant_id=t.id
    JOIN identity.sessions s ON s.id=NEW.initiator_session_id
    WHERE t.id=NEW.tenant_id AND t.kind='enterprise' AND t.status='active'
      AND m.id=NEW.initiator_membership_id AND m.user_id=NEW.initiator_user_id
      AND m.status='active' AND m.role IN ('owner','contract_admin')
      AND s.user_id=NEW.initiator_user_id AND s.active_tenant_id=NEW.tenant_id
      AND s.revoked_at IS NULL AND s.expires_at>NEW.created_at
      AND s.reauthenticated_at=NEW.reauthenticated_at
  ) INTO valid_principal;
  IF NOT valid_principal THEN RAISE EXCEPTION 'contract proposer is not currently authorized' USING ERRCODE='42501'; END IF;
  IF NEW.action='create' THEN
    IF EXISTS(SELECT 1 FROM contracts.contracts WHERE tenant_id=NEW.tenant_id AND id=NEW.target_contract_id) THEN
      RAISE EXCEPTION 'contract create target already exists' USING ERRCODE='23505';
    END IF;
  ELSE
    SELECT * INTO target FROM contracts.contracts
      WHERE tenant_id=NEW.tenant_id AND id=NEW.target_contract_id;
    IF NOT FOUND OR target.version<>NEW.target_version OR target.status='terminated' THEN
      RAISE EXCEPTION 'contract proposal target version changed' USING ERRCODE='40001';
    END IF;
  END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER contract_proposals_validate BEFORE INSERT ON contracts.contract_proposals
  FOR EACH ROW EXECUTE FUNCTION contracts.validate_contract_proposal();

CREATE FUNCTION contracts.validate_contract_approval_decision() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,contracts,identity SET row_security=off AS $$
DECLARE proposal contracts.contract_proposals%ROWTYPE; valid_principal boolean;
BEGIN
  SELECT * INTO proposal FROM contracts.contract_proposals
    WHERE tenant_id=NEW.tenant_id AND id=NEW.proposal_id FOR UPDATE;
  IF NOT FOUND OR proposal.status<>'proposed' OR proposal.expires_at<=NEW.decided_at
    OR NEW.decided_at<proposal.created_at
    OR NEW.decided_at<statement_timestamp()-interval '1 minute'
    OR NEW.decided_at>statement_timestamp()+interval '1 minute'
    OR proposal.initiator_user_id=NEW.approver_user_id
    OR proposal.proposal_hash<>NEW.proposal_hash OR proposal.target_version<>NEW.target_version THEN
    RAISE EXCEPTION 'contract approval scope changed' USING ERRCODE='40001';
  END IF;
  IF NEW.decision='approve' AND (SELECT count(*) FROM contracts.contract_approval_decisions d
      WHERE d.tenant_id=NEW.tenant_id AND d.proposal_id=NEW.proposal_id AND d.decision='approve')>=2 THEN
    RAISE EXCEPTION 'contract proposal already has two approvals' USING ERRCODE='23505';
  END IF;
  SELECT EXISTS(
    SELECT 1 FROM identity.memberships m JOIN identity.sessions s ON s.id=NEW.session_id
    WHERE m.tenant_id=NEW.tenant_id AND m.id=NEW.membership_id AND m.user_id=NEW.approver_user_id
      AND m.status='active' AND m.role IN ('owner','contract_admin')
      AND s.user_id=NEW.approver_user_id AND s.active_tenant_id=NEW.tenant_id
      AND s.revoked_at IS NULL AND s.expires_at>NEW.decided_at
      AND s.reauthenticated_at=NEW.reauthenticated_at
  ) INTO valid_principal;
  IF NOT valid_principal THEN RAISE EXCEPTION 'contract approver is not currently authorized' USING ERRCODE='42501'; END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER contract_approval_decisions_validate BEFORE INSERT ON contracts.contract_approval_decisions
  FOR EACH ROW EXECUTE FUNCTION contracts.validate_contract_approval_decision();

CREATE FUNCTION contracts.finalize_contract_rejection() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,contracts SET row_security=off AS $$
BEGIN
  IF NEW.decision='reject' THEN
    UPDATE contracts.contract_proposals
      SET version=2,status='rejected',updated_at=NEW.decided_at
      WHERE tenant_id=NEW.tenant_id AND id=NEW.proposal_id AND version=1 AND status='proposed';
    IF NOT FOUND THEN RAISE EXCEPTION 'contract proposal rejection lost its version fence' USING ERRCODE='40001'; END IF;
  END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER contract_approval_decisions_reject AFTER INSERT ON contracts.contract_approval_decisions
  FOR EACH ROW EXECUTE FUNCTION contracts.finalize_contract_rejection();

CREATE FUNCTION contracts.enforce_contract_lifecycle() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,contracts SET row_security=off AS $$
DECLARE proposal_id uuid; authorized boolean;
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'contract deletion is forbidden'; END IF;
  proposal_id:=NULLIF(current_setting('lites.contract_proposal_id',true),'')::uuid;
  IF proposal_id IS NULL THEN RAISE EXCEPTION 'contract mutation requires approved proposal' USING ERRCODE='42501'; END IF;
  IF TG_OP='INSERT' THEN
    SELECT EXISTS(SELECT 1 FROM contracts.contract_proposals p WHERE p.id=proposal_id AND p.tenant_id=NEW.tenant_id
      AND p.target_contract_id=NEW.id AND p.action='create' AND p.status='proposed' AND p.target_version=0)
      INTO authorized;
    IF NOT authorized THEN RAISE EXCEPTION 'contract create proposal mismatch' USING ERRCODE='42501'; END IF;
    RETURN NEW;
  END IF;
  SELECT EXISTS(SELECT 1 FROM contracts.contract_proposals p WHERE p.id=proposal_id AND p.tenant_id=OLD.tenant_id
    AND p.target_contract_id=OLD.id AND p.status='proposed' AND p.target_version=OLD.version)
    INTO authorized;
  IF NOT authorized OR NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.created_at IS DISTINCT FROM OLD.created_at OR NEW.activated_at IS DISTINCT FROM OLD.activated_at
    OR NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at OR OLD.status='terminated'
  THEN RAISE EXCEPTION 'invalid approved contract transition'; END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER contracts_lifecycle BEFORE INSERT OR UPDATE OR DELETE ON contracts.contracts
  FOR EACH ROW EXECUTE FUNCTION contracts.enforce_contract_lifecycle();

ALTER TABLE contracts.seat_allocations
  ADD CONSTRAINT seat_allocations_tenant_id_id_unique UNIQUE (tenant_id,id),
  ADD CONSTRAINT seat_allocations_contract_exact_fk FOREIGN KEY (tenant_id,contract_id)
    REFERENCES contracts.contracts(tenant_id,id) ON DELETE RESTRICT,
  ADD CONSTRAINT seat_allocations_membership_exact_fk FOREIGN KEY (tenant_id,membership_id)
    REFERENCES identity.memberships(tenant_id,id) ON DELETE RESTRICT,
  ADD CONSTRAINT seat_allocations_lifecycle_contract CHECK (
    status IN ('active','released') AND (status='active')=(released_at IS NULL)
    AND (released_at IS NULL OR released_at>=allocated_at)
  );
CREATE UNIQUE INDEX seat_allocations_one_active_membership_idx
  ON contracts.seat_allocations(tenant_id,membership_id) WHERE status='active';

CREATE FUNCTION contracts.enforce_seat_allocation_lifecycle() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog,contracts AS $$
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'seat allocation deletion is forbidden'; END IF;
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.contract_id IS DISTINCT FROM OLD.contract_id OR NEW.membership_id IS DISTINCT FROM OLD.membership_id
    OR NEW.created_at IS DISTINCT FROM OLD.created_at OR NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at
    OR NOT ((OLD.status='active' AND NEW.status='released' AND NEW.released_at IS NOT NULL
        AND NEW.allocated_at IS NOT DISTINCT FROM OLD.allocated_at)
      OR (OLD.status='released' AND NEW.status='active' AND NEW.released_at IS NULL
        AND NEW.allocated_at>=OLD.updated_at))
  THEN RAISE EXCEPTION 'invalid seat allocation transition'; END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER seat_allocations_lifecycle BEFORE UPDATE OR DELETE ON contracts.seat_allocations
  FOR EACH ROW EXECUTE FUNCTION contracts.enforce_seat_allocation_lifecycle();

CREATE FUNCTION contracts.sync_membership_seat() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,contracts,identity SET row_security=off AS $$
DECLARE active_contract contracts.contracts%ROWTYPE; active_seats integer; allocation_id uuid;
BEGIN
  IF TG_OP='UPDATE' AND OLD.status='active' AND NEW.status<>'active' THEN
    UPDATE contracts.seat_allocations SET version=version+1,status='released',released_at=NEW.updated_at,updated_at=NEW.updated_at
      WHERE tenant_id=NEW.tenant_id AND membership_id=NEW.id AND status='active';
    RETURN NEW;
  END IF;
  IF NEW.status<>'active' OR (TG_OP='UPDATE' AND OLD.status='active') THEN RETURN NEW; END IF;
  SELECT c.* INTO active_contract FROM contracts.contracts c
    WHERE c.tenant_id=NEW.tenant_id AND c.status='active' AND c.starts_at<=NEW.updated_at AND c.ends_at>NEW.updated_at
    FOR UPDATE;
  IF NOT FOUND THEN RETURN NEW; END IF;
  SELECT count(*) INTO active_seats FROM contracts.seat_allocations
    WHERE tenant_id=NEW.tenant_id AND contract_id=active_contract.id AND status='active';
  IF active_seats>=active_contract.seat_limit THEN
    RAISE EXCEPTION 'contract seat limit exceeded' USING ERRCODE='23514';
  END IF;
  allocation_id:=md5(NEW.tenant_id::text||active_contract.id::text||NEW.id::text)::uuid;
  INSERT INTO contracts.seat_allocations(id,tenant_id,contract_id,membership_id,status,allocated_at,created_at,updated_at)
    VALUES(allocation_id,NEW.tenant_id,active_contract.id,NEW.id,'active',NEW.updated_at,NEW.updated_at,NEW.updated_at)
    ON CONFLICT (tenant_id,contract_id,membership_id) DO UPDATE
      SET version=contracts.seat_allocations.version+1,status='active',released_at=NULL,allocated_at=EXCLUDED.allocated_at,updated_at=EXCLUDED.updated_at
      WHERE contracts.seat_allocations.status='released';
  RETURN NEW;
END
$$;
CREATE TRIGGER memberships_contract_seat AFTER INSERT OR UPDATE OF status ON identity.memberships
  FOR EACH ROW EXECUTE FUNCTION contracts.sync_membership_seat();

CREATE FUNCTION contracts.apply_approved_contract_proposal(
  p_tenant_id uuid,p_proposal_id uuid,p_actor_user_id uuid,p_audit_id uuid,p_now timestamptz
) RETURNS TABLE(contract_id uuid,contract_version bigint,contract_status text)
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,contracts,identity SET row_security=off AS $$
DECLARE proposal contracts.contract_proposals%ROWTYPE; current_contract contracts.contracts%ROWTYPE;
  approvals integer; approval_events jsonb; member_count integer;
BEGIN
  IF NULLIF(current_setting('lites.tenant_id',true),'')::uuid IS DISTINCT FROM p_tenant_id
    OR p_proposal_id IS NULL OR p_actor_user_id IS NULL OR p_audit_id IS NULL OR p_now IS NULL THEN
    RAISE EXCEPTION 'invalid contract activation arguments' USING ERRCODE='42501';
  END IF;
  IF p_now<statement_timestamp()-interval '1 minute' OR p_now>statement_timestamp()+interval '1 minute' THEN
    RAISE EXCEPTION 'contract activation timestamp is outside the accepted clock window' USING ERRCODE='22008';
  END IF;
  SELECT * INTO proposal FROM contracts.contract_proposals
    WHERE tenant_id=p_tenant_id AND id=p_proposal_id FOR UPDATE;
  IF NOT FOUND OR proposal.status<>'proposed' OR proposal.created_at>p_now OR proposal.expires_at<=p_now THEN
    RAISE EXCEPTION 'contract proposal is not executable' USING ERRCODE='40001';
  END IF;
  IF NOT EXISTS(
    SELECT 1 FROM identity.memberships m JOIN identity.sessions s ON s.id=proposal.initiator_session_id
    WHERE m.tenant_id=p_tenant_id AND m.id=proposal.initiator_membership_id
      AND m.user_id=proposal.initiator_user_id AND m.status='active' AND m.role IN ('owner','contract_admin')
      AND s.user_id=proposal.initiator_user_id AND s.active_tenant_id=p_tenant_id
      AND s.revoked_at IS NULL AND s.expires_at>p_now AND s.reauthenticated_at=proposal.reauthenticated_at
  ) THEN RAISE EXCEPTION 'contract proposer authorization changed' USING ERRCODE='42501'; END IF;
  SELECT count(*),jsonb_agg(d.event_id ORDER BY d.decided_at,d.id) INTO approvals,approval_events
    FROM contracts.contract_approval_decisions d
    JOIN identity.memberships m ON m.tenant_id=d.tenant_id AND m.id=d.membership_id
      AND m.user_id=d.approver_user_id AND m.status='active' AND m.role IN ('owner','contract_admin')
    JOIN identity.sessions s ON s.id=d.session_id AND s.user_id=d.approver_user_id
      AND s.active_tenant_id=d.tenant_id AND s.revoked_at IS NULL AND s.expires_at>p_now
      AND s.reauthenticated_at=d.reauthenticated_at
    WHERE d.tenant_id=p_tenant_id AND d.proposal_id=p_proposal_id AND d.decision='approve'
      AND d.proposal_hash=proposal.proposal_hash AND d.target_version=proposal.target_version
      AND d.decided_at<=p_now;
  IF approvals<>2 OR NOT EXISTS(SELECT 1 FROM contracts.contract_approval_decisions d
      WHERE d.tenant_id=p_tenant_id AND d.proposal_id=p_proposal_id AND d.approver_user_id=p_actor_user_id AND d.decision='approve') THEN
    RAISE EXCEPTION 'two current contract approvals are required' USING ERRCODE='42501';
  END IF;
  PERFORM set_config('lites.contract_proposal_id',p_proposal_id::text,true);
  IF proposal.action='create' THEN
    SELECT count(*) INTO member_count FROM identity.memberships WHERE tenant_id=p_tenant_id AND status='active';
    IF member_count>proposal.seat_limit THEN RAISE EXCEPTION 'contract seat limit is below active membership count' USING ERRCODE='23514'; END IF;
    INSERT INTO contracts.contracts(id,tenant_id,contract_number,status,starts_at,ends_at,seat_limit,region,license_kind,proposal_hash,activated_at,created_at,updated_at)
      VALUES(proposal.target_contract_id,p_tenant_id,proposal.contract_number,'active',proposal.starts_at,proposal.ends_at,proposal.seat_limit,proposal.region,proposal.license_kind,proposal.proposal_hash,p_now,p_now,p_now)
      RETURNING * INTO current_contract;
    INSERT INTO contracts.seat_allocations(id,tenant_id,contract_id,membership_id,status,allocated_at,created_at,updated_at)
      SELECT md5(p_tenant_id::text||current_contract.id::text||m.id::text)::uuid,p_tenant_id,current_contract.id,m.id,'active',p_now,p_now,p_now
      FROM identity.memberships m WHERE m.tenant_id=p_tenant_id AND m.status='active' ORDER BY m.id;
  ELSE
    SELECT * INTO current_contract FROM contracts.contracts
      WHERE tenant_id=p_tenant_id AND id=proposal.target_contract_id FOR UPDATE;
    IF NOT FOUND OR current_contract.version<>proposal.target_version OR current_contract.status='terminated' THEN
      RAISE EXCEPTION 'contract target version changed' USING ERRCODE='40001';
    END IF;
    IF proposal.action='renew' THEN
      SELECT count(*) INTO member_count FROM identity.memberships WHERE tenant_id=p_tenant_id AND status='active';
      IF member_count>proposal.seat_limit THEN RAISE EXCEPTION 'renewed seat limit is below active membership count' USING ERRCODE='23514'; END IF;
      UPDATE contracts.contracts SET version=version+1,contract_number=proposal.contract_number,status='active',starts_at=proposal.starts_at,
        ends_at=proposal.ends_at,seat_limit=proposal.seat_limit,region=proposal.region,license_kind=proposal.license_kind,
        proposal_hash=proposal.proposal_hash,updated_at=p_now WHERE tenant_id=p_tenant_id AND id=current_contract.id RETURNING * INTO current_contract;
      INSERT INTO contracts.seat_allocations(id,tenant_id,contract_id,membership_id,status,allocated_at,created_at,updated_at)
        SELECT md5(p_tenant_id::text||current_contract.id::text||m.id::text)::uuid,p_tenant_id,current_contract.id,m.id,'active',p_now,p_now,p_now
        FROM identity.memberships m WHERE m.tenant_id=p_tenant_id AND m.status='active' ORDER BY m.id
        ON CONFLICT ON CONSTRAINT seat_allocations_tenant_id_contract_id_membership_id_key DO UPDATE
          SET version=contracts.seat_allocations.version+1,status='active',released_at=NULL,allocated_at=EXCLUDED.allocated_at,updated_at=EXCLUDED.updated_at
          WHERE contracts.seat_allocations.status='released';
    ELSIF proposal.action='suspend' THEN
      UPDATE contracts.contracts SET version=version+1,status='suspended',proposal_hash=proposal.proposal_hash,updated_at=p_now
        WHERE tenant_id=p_tenant_id AND id=current_contract.id RETURNING * INTO current_contract;
    ELSE
      UPDATE contracts.contracts SET version=version+1,status='terminated',proposal_hash=proposal.proposal_hash,updated_at=p_now
        WHERE tenant_id=p_tenant_id AND id=current_contract.id RETURNING * INTO current_contract;
      UPDATE contracts.seat_allocations SET version=version+1,status='released',released_at=p_now,updated_at=p_now
        WHERE tenant_id=p_tenant_id AND contract_id=current_contract.id AND status='active';
    END IF;
  END IF;
  UPDATE contracts.contract_proposals SET version=2,status='executed',executed_at=p_now,updated_at=p_now
    WHERE tenant_id=p_tenant_id AND id=p_proposal_id AND version=1 AND status='proposed';
  INSERT INTO contracts.contract_audit_events(id,tenant_id,contract_id,actor_user_id,event_type,reason,before_version,after_version,proposal_hash,approval_event_ids,occurred_at)
    VALUES(p_audit_id,p_tenant_id,current_contract.id,p_actor_user_id,'contract_'||proposal.action||'_executed',proposal.reason_hash,
      CASE WHEN proposal.target_version=0 THEN NULL ELSE proposal.target_version END,current_contract.version,proposal.proposal_hash,approval_events,p_now);
  RETURN QUERY SELECT current_contract.id,current_contract.version,current_contract.status;
END
$$;
REVOKE ALL ON FUNCTION contracts.apply_approved_contract_proposal(uuid,uuid,uuid,uuid,timestamptz) FROM PUBLIC;

ALTER TABLE contracts.contract_audit_events
  ADD CONSTRAINT contract_audit_events_contract_exact_fk FOREIGN KEY (tenant_id,contract_id)
    REFERENCES contracts.contracts(tenant_id,id) ON DELETE RESTRICT,
  ADD CONSTRAINT contract_audit_events_completeness CHECK (
    length(btrim(event_type))>0 AND length(btrim(reason))>0
    AND after_version>0 AND proposal_hash ~ '^[0-9a-f]{64}$'
    AND jsonb_typeof(approval_event_ids)='array' AND jsonb_array_length(approval_event_ids)=2
  );

CREATE INDEX contract_proposals_status_expiry_idx ON contracts.contract_proposals(tenant_id,status,expires_at,id);
CREATE INDEX contract_approval_decisions_proposal_idx ON contracts.contract_approval_decisions(tenant_id,proposal_id,decided_at,id);
CREATE INDEX seat_allocations_contract_status_idx ON contracts.seat_allocations(tenant_id,contract_id,status,membership_id);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_contract_service') THEN
    GRANT USAGE ON SCHEMA identity,agent,contracts TO lites_contract_service;
    GRANT SELECT ON identity.tenants,identity.memberships,identity.sessions TO lites_contract_service;
    GRANT SELECT ON contracts.contracts,contracts.seat_allocations,contracts.contract_audit_events TO lites_contract_service;
    GRANT SELECT,INSERT ON contracts.contract_proposals,contracts.contract_approval_decisions TO lites_contract_service;
    GRANT SELECT,INSERT,UPDATE ON agent.idempotency_responses,agent.outbox TO lites_contract_service;
    GRANT SELECT,INSERT ON agent.events TO lites_contract_service;
    GRANT EXECUTE ON FUNCTION contracts.apply_approved_contract_proposal(uuid,uuid,uuid,uuid,timestamptz) TO lites_contract_service;
  END IF;
END
$$;
