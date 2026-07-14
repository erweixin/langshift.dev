DROP INDEX IF EXISTS product.artifact_revision_evidence_evidence_idx;
DROP INDEX IF EXISTS product.artifact_revisions_manifest_idx;
DROP TRIGGER IF EXISTS artifacts_lifecycle ON product.artifacts;
DROP FUNCTION IF EXISTS product.enforce_artifact_lifecycle();
DROP TABLE IF EXISTS product.artifact_revision_evidence;

ALTER TABLE product.artifacts
  DROP CONSTRAINT IF EXISTS artifacts_current_revision_fk;

ALTER TABLE product.artifact_revisions
  DROP CONSTRAINT IF EXISTS artifact_revisions_created_event_fk,
  DROP CONSTRAINT IF EXISTS artifact_revisions_tenant_project_fk,
  DROP CONSTRAINT IF EXISTS artifact_revisions_tenant_artifact_fk,
  DROP CONSTRAINT IF EXISTS artifact_revisions_scope_contract,
  DROP CONSTRAINT IF EXISTS artifact_revisions_content_unique,
  DROP CONSTRAINT IF EXISTS artifact_revisions_tenant_id_id_unique,
  DROP COLUMN IF EXISTS created_event_id,
  DROP COLUMN IF EXISTS scan_result_hash,
  DROP COLUMN IF EXISTS evidence_manifest_hash,
  DROP COLUMN IF EXISTS byte_size,
  DROP COLUMN IF EXISTS media_type,
  DROP COLUMN IF EXISTS object_version,
  DROP COLUMN IF EXISTS workspace_revision,
  DROP COLUMN IF EXISTS project_id;

ALTER TABLE product.artifacts
  DROP CONSTRAINT IF EXISTS artifacts_current_revision_contract,
  DROP CONSTRAINT IF EXISTS artifacts_scope_contract,
  DROP CONSTRAINT IF EXISTS artifacts_status_contract,
  DROP CONSTRAINT IF EXISTS artifacts_tenant_id_id_unique,
  DROP COLUMN IF EXISTS archived_at,
  DROP COLUMN IF EXISTS current_content_hash,
  DROP COLUMN IF EXISTS current_revision_id,
  DROP COLUMN IF EXISTS title;

ALTER TABLE product.projects DROP CONSTRAINT IF EXISTS projects_tenant_id_id_unique;
ALTER TABLE product.evidence
  DROP CONSTRAINT IF EXISTS evidence_immutable_binding_unique,
  DROP CONSTRAINT IF EXISTS evidence_tenant_id_id_unique;
