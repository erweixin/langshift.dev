DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM agent.memory_documents)
    OR EXISTS (SELECT 1 FROM agent.retrieval_manifests)
    OR EXISTS (SELECT 1 FROM agent.memory_index_projections) THEN
    RAISE EXCEPTION 'memory protocol downgrade backfill required' USING ERRCODE='55000';
  END IF;
END
$$;

ALTER TABLE agent.tool_calls
  DROP CONSTRAINT IF EXISTS tool_calls_execution_mode_contract,
  DROP COLUMN IF EXISTS execution_mode;

DROP TRIGGER IF EXISTS retrieval_manifest_commit_guard ON agent.retrieval_manifests;
DROP FUNCTION IF EXISTS agent.validate_retrieval_manifest_commit();
DROP TRIGGER IF EXISTS memory_tombstone_commit_guard ON agent.memory_tombstones;
DROP FUNCTION IF EXISTS agent.validate_memory_tombstone_commit();
DROP TRIGGER IF EXISTS memory_revision_commit_guard ON agent.memory_document_revisions;
DROP FUNCTION IF EXISTS agent.validate_memory_revision_commit();
DROP TRIGGER IF EXISTS memory_index_projections_lifecycle ON agent.memory_index_projections;
DROP FUNCTION IF EXISTS agent.enforce_memory_index_projection_lifecycle();

DROP TABLE IF EXISTS agent.memory_accesses;
DROP TABLE IF EXISTS agent.retrieval_manifest_chunks;
DROP TABLE IF EXISTS agent.retrieval_manifests;
DROP TABLE IF EXISTS agent.memory_index_projections;
DROP TABLE IF EXISTS agent.memory_tombstones;

DROP INDEX IF EXISTS agent.memory_documents_active_expiration_scan_idx;
DROP INDEX IF EXISTS agent.memory_documents_scope_active_idx;

ALTER TABLE agent.memory_documents DROP CONSTRAINT IF EXISTS memory_documents_current_revision_fk;
DROP TABLE IF EXISTS agent.memory_revision_derivations;
DROP TABLE IF EXISTS agent.memory_revision_subjects;
DROP TABLE IF EXISTS agent.memory_revision_sources;
DROP TABLE IF EXISTS agent.memory_document_revisions;

DROP TRIGGER IF EXISTS memory_documents_lifecycle ON agent.memory_documents;
DROP FUNCTION IF EXISTS agent.enforce_memory_document_lifecycle();

ALTER TABLE agent.memory_documents
  DROP CONSTRAINT IF EXISTS memory_documents_superseded_by_fk,
  DROP CONSTRAINT IF EXISTS memory_documents_deleted_event_fk,
  DROP CONSTRAINT IF EXISTS memory_documents_upserted_event_fk,
  DROP CONSTRAINT IF EXISTS memory_documents_lifecycle_contract,
  DROP CONSTRAINT IF EXISTS memory_documents_scope_contract,
  DROP CONSTRAINT IF EXISTS memory_documents_tenant_id_id_version_unique,
  DROP CONSTRAINT IF EXISTS memory_documents_tenant_id_id_unique,
  DROP COLUMN deleted_event_id,
  DROP COLUMN upserted_event_id,
  DROP COLUMN deleted_at,
  DROP COLUMN superseded_by,
  DROP COLUMN current_revision,
  DROP COLUMN scope_id,
  DROP COLUMN scope_kind,
  ADD COLUMN memory_kind text,
  ADD COLUMN source_event_id uuid,
  ADD COLUMN revision integer,
  ADD COLUMN payload_ref text,
  ADD COLUMN content_hash text,
  ADD COLUMN embedding_ref text;

ALTER TABLE agent.memory_documents
  ALTER COLUMN memory_kind SET NOT NULL,
  ALTER COLUMN source_event_id SET NOT NULL,
  ALTER COLUMN revision SET NOT NULL,
  ALTER COLUMN payload_ref SET NOT NULL,
  ALTER COLUMN content_hash SET NOT NULL,
  ADD CONSTRAINT memory_documents_tenant_id_id_revision_key UNIQUE (tenant_id,id,revision);
