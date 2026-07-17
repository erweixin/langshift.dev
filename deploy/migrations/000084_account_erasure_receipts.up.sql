BEGIN;

CREATE TABLE identity.account_erasure_receipts (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  request_id uuid NOT NULL,
  user_id uuid NOT NULL,
  recovery_epoch uuid NOT NULL,
  surface text NOT NULL,
  receipt_hash text NOT NULL,
  details jsonb NOT NULL,
  erased_at timestamptz NOT NULL,
  UNIQUE (tenant_id,request_id,recovery_epoch,surface),
  CONSTRAINT account_erasure_receipts_request_fk FOREIGN KEY (request_id) REFERENCES identity.account_erasure_requests(id) ON DELETE RESTRICT,
  CONSTRAINT account_erasure_receipts_contract CHECK (
    surface IN ('payload','memory','indexes','workspace_artifact','cache','snapshot')
    AND receipt_hash ~ '^[0-9a-f]{64}$'
    AND jsonb_typeof(details)='object'
  )
);

CREATE TABLE identity.subject_erasure_tombstones (
  request_id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  initial_recovery_epoch uuid NOT NULL,
  receipt_manifest_hash text NOT NULL,
  completed_at timestamptz NOT NULL,
  UNIQUE (tenant_id,user_id),
  CONSTRAINT subject_erasure_tombstones_request_fk FOREIGN KEY (request_id) REFERENCES identity.account_erasure_requests(id) ON DELETE RESTRICT,
  CONSTRAINT subject_erasure_tombstones_contract CHECK (receipt_manifest_hash ~ '^[0-9a-f]{64}$')
);

ALTER TABLE identity.account_erasure_receipts ENABLE ROW LEVEL SECURITY;
ALTER TABLE identity.account_erasure_receipts FORCE ROW LEVEL SECURITY;
CREATE POLICY account_erasure_receipts_tenant_isolation ON identity.account_erasure_receipts
  USING (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid);
ALTER TABLE identity.subject_erasure_tombstones ENABLE ROW LEVEL SECURITY;
ALTER TABLE identity.subject_erasure_tombstones FORCE ROW LEVEL SECURITY;
CREATE POLICY subject_erasure_tombstones_tenant_isolation ON identity.subject_erasure_tombstones
  USING (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid);

CREATE TRIGGER account_erasure_receipts_append_only BEFORE UPDATE OR DELETE ON identity.account_erasure_receipts
  FOR EACH ROW EXECUTE FUNCTION agent.reject_append_only_mutation();
CREATE TRIGGER subject_erasure_tombstones_append_only BEFORE UPDATE OR DELETE ON identity.subject_erasure_tombstones
  FOR EACH ROW EXECUTE FUNCTION agent.reject_append_only_mutation();
CREATE INDEX account_erasure_receipts_restore_idx ON identity.account_erasure_receipts(tenant_id,recovery_epoch,request_id,surface);
CREATE INDEX subject_erasure_tombstones_restore_idx ON identity.subject_erasure_tombstones(completed_at,tenant_id,request_id);

CREATE OR REPLACE FUNCTION identity.list_subject_erasure_tombstones_missing_epoch(
  p_recovery_epoch uuid,
  p_limit integer
) RETURNS TABLE(
  id uuid,
  tenant_id uuid,
  user_id uuid,
  version bigint,
  status text,
  scheduled_for timestamptz
) LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,identity
AS $$
BEGIN
  IF p_recovery_epoch IS NULL OR p_limit < 1 OR p_limit > 1000 THEN
    RAISE EXCEPTION 'invalid subject erasure restore scan' USING ERRCODE='22023';
  END IF;
  RETURN QUERY
    SELECT r.id,r.tenant_id,r.user_id,r.version,r.status,r.scheduled_for
    FROM identity.subject_erasure_tombstones t
    JOIN identity.account_erasure_requests r ON r.id=t.request_id AND r.tenant_id=t.tenant_id
    WHERE r.status='completed'
      AND (SELECT count(*) FROM identity.account_erasure_receipts x
           WHERE x.tenant_id=t.tenant_id AND x.request_id=t.request_id
             AND x.recovery_epoch=p_recovery_epoch) < 6
    ORDER BY t.completed_at,t.tenant_id,t.request_id
    LIMIT p_limit;
END
$$;
REVOKE ALL ON FUNCTION identity.list_subject_erasure_tombstones_missing_epoch(uuid,integer) FROM PUBLIC;

CREATE OR REPLACE FUNCTION identity.list_subject_erasure_tenants(p_user_id uuid)
RETURNS TABLE(tenant_id uuid) LANGUAGE plpgsql STABLE SECURITY DEFINER
SET search_path=pg_catalog,identity SET row_security=off AS $$
BEGIN
  IF p_user_id IS NULL THEN
    RAISE EXCEPTION 'invalid subject erasure tenant scan' USING ERRCODE='22023';
  END IF;
  RETURN QUERY
    SELECT m.tenant_id FROM identity.memberships m WHERE m.user_id=p_user_id
    UNION
    SELECT r.tenant_id FROM identity.account_erasure_requests r WHERE r.user_id=p_user_id
    ORDER BY 1;
END
$$;
REVOKE ALL ON FUNCTION identity.list_subject_erasure_tenants(uuid) FROM PUBLIC;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_agent_service') THEN
    GRANT USAGE ON SCHEMA identity TO lites_agent_service;
    GRANT SELECT ON identity.subject_erasure_tombstones TO lites_agent_service;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_erasure_worker') THEN
    GRANT USAGE ON SCHEMA identity,product,agent TO lites_erasure_worker;
    GRANT EXECUTE ON FUNCTION identity.list_subject_erasure_tombstones_missing_epoch(uuid,integer),identity.list_subject_erasure_tenants(uuid) TO lites_erasure_worker;
    GRANT SELECT,UPDATE ON identity.account_erasure_requests TO lites_erasure_worker;
    GRANT SELECT,INSERT ON identity.account_erasure_receipts,identity.subject_erasure_tombstones,identity.security_events TO lites_erasure_worker;
    GRANT SELECT,UPDATE ON identity.users,identity.memberships,identity.consent_records TO lites_erasure_worker;
    GRANT SELECT,DELETE ON identity.password_credentials,identity.email_verifications,identity.password_reset_requests,identity.sessions,identity.invitations TO lites_erasure_worker;
    GRANT SELECT ON identity.onboarding_sessions TO lites_erasure_worker;
    GRANT SELECT ON product.missions,product.route_revisions,product.evidence,product.daily_tasks,product.submissions,product.reviews,product.projects,product.artifact_revisions,product.portfolio_exports,product.data_export_requests,product.support_cases,product.support_case_messages TO lites_erasure_worker;
    GRANT SELECT ON agent.memory_documents,agent.memory_document_revisions,agent.memory_revision_subjects,agent.memory_revision_derivations,agent.memory_index_projections,agent.retrieval_manifests,agent.retrieval_manifest_chunks,agent.coach_context_snapshots,agent.snapshots,agent.runs,agent.run_messages,agent.tool_calls,agent.idempotency_responses TO lites_erasure_worker;
    GRANT SELECT,INSERT,UPDATE ON agent.event_cursors,agent.outbox,agent.inbox TO lites_erasure_worker;
    GRANT SELECT,INSERT ON agent.events TO lites_erasure_worker;
  END IF;
END
$$;

COMMIT;
