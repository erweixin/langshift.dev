BEGIN;

ALTER TABLE product.data_export_requests
  ADD COLUMN failure_code text;

UPDATE product.data_export_requests
SET failure_code='legacy_failure'
WHERE status='failed' AND failure_code IS NULL;

ALTER TABLE product.data_export_requests
  ADD CONSTRAINT data_export_requests_scope_contract CHECK (
    jsonb_typeof(scope)='object'
    AND jsonb_typeof(scope->'categories')='array'
    AND jsonb_array_length(scope->'categories') BETWEEN 1 AND 7
    AND scope->>'format' IN ('json','zip')
  ) NOT VALID,
  ADD CONSTRAINT data_export_requests_lifecycle_contract CHECK (
    (status='requested' AND object_ref IS NULL AND content_hash IS NULL AND failure_code IS NULL
      AND expires_at IS NULL AND completed_at IS NULL)
    OR (status='ready' AND NULLIF(object_ref,'') IS NOT NULL
      AND content_hash ~ '^[0-9a-f]{64}$' AND expires_at>completed_at
      AND completed_at IS NOT NULL AND failure_code IS NULL)
    OR (status='failed' AND object_ref IS NULL AND content_hash IS NULL
      AND expires_at IS NULL AND completed_at IS NOT NULL
      AND failure_code IN ('invalid_command','archive_too_large','legacy_failure'))
  ) NOT VALID;

ALTER TABLE product.data_export_requests
  VALIDATE CONSTRAINT data_export_requests_scope_contract;
ALTER TABLE product.data_export_requests
  VALIDATE CONSTRAINT data_export_requests_lifecycle_contract;

CREATE INDEX data_export_requests_owner_created_idx
  ON product.data_export_requests(tenant_id,user_id,created_at DESC,id DESC);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_erasure_worker') THEN
    GRANT SELECT ON identity.users,identity.memberships,identity.tenants,identity.security_events TO lites_erasure_worker;
    GRANT SELECT ON product.missions,product.mission_focuses,product.route_revisions,
      product.capability_claims,product.claim_evidence_links,
      product.daily_tasks,product.submissions,product.reviews,product.evidence,
      product.projects,product.artifacts,product.artifact_revisions,product.portfolio_exports
      TO lites_erasure_worker;
    GRANT SELECT ON agent.conversations,agent.run_messages,
      agent.memory_documents,agent.memory_document_revisions TO lites_erasure_worker;
    GRANT SELECT,UPDATE ON product.data_export_requests TO lites_erasure_worker;
  END IF;
END
$$;

COMMIT;
