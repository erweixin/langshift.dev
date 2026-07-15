\set ON_ERROR_STOP on
BEGIN;
DO $verify$
DECLARE actual integer; memory_guard text; retrieval_guard text;
BEGIN
  SELECT count(*) INTO actual FROM public.lites_schema_migrations;
  IF actual<>28 OR NOT EXISTS (
    SELECT 1 FROM public.lites_schema_migrations WHERE version=28 AND name='memory_retrieval_protocol'
  ) THEN RAISE EXCEPTION 'expected migration version 28'; END IF;

  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='memory_documents_current_revision_fk' AND condeferrable) THEN
    RAISE EXCEPTION 'memory current revision binding is absent';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='memory_document_revisions_append_only' AND NOT tgisinternal) THEN
    RAISE EXCEPTION 'memory revision append-only boundary is absent';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='memory_revision_commit_guard' AND tgdeferrable AND tginitdeferred) THEN
    RAISE EXCEPTION 'governed memory commit guard is absent';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='tool_calls_execution_mode_contract') THEN
    RAISE EXCEPTION 'inline platform ToolCall mode boundary is absent';
  END IF;
  SELECT pg_get_functiondef('agent.validate_memory_revision_commit()'::regprocedure) INTO memory_guard;
  IF position('memory_write' IN memory_guard)=0 OR position('ToolCallSucceeded' IN memory_guard)=0
    OR position('MemoryUpserted' IN memory_guard)=0 OR position('memory_policies' IN memory_guard)=0
    OR position('inline_platform' IN memory_guard)=0 THEN
    RAISE EXCEPTION 'memory commit guard lacks exact tool, event or policy binding';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='memory_revision_subjects_revision_fk')
    OR NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='memory_revision_derivations_source_fk') THEN
    RAISE EXCEPTION 'memory erasure or derivation lineage is absent';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='memory_tombstone_commit_guard' AND tgdeferrable AND tginitdeferred) THEN
    RAISE EXCEPTION 'memory deletion fact guard is absent';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='memory_index_projections_lifecycle' AND NOT tgisinternal) THEN
    RAISE EXCEPTION 'memory index lifecycle is absent';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='retrieval_manifest_commit_guard' AND tgdeferrable AND tginitdeferred) THEN
    RAISE EXCEPTION 'retrieval manifest commit guard is absent';
  END IF;
  SELECT pg_get_functiondef('agent.validate_retrieval_manifest_commit()'::regprocedure) INTO retrieval_guard;
  IF position('context_manifest' IN retrieval_guard)=0 OR position('RetrievalManifestCommitted' IN retrieval_guard)=0
    OR position('memory_index_projections' IN retrieval_guard)=0 OR position('share_grants' IN retrieval_guard)=0 THEN
    RAISE EXCEPTION 'retrieval manifest lacks exact context, projection or ACL binding';
  END IF;
  IF EXISTS (
    SELECT 1 FROM (VALUES
      ('agent.memory_document_revisions'::regclass),('agent.memory_revision_sources'::regclass),
      ('agent.memory_revision_subjects'::regclass),('agent.memory_revision_derivations'::regclass),
      ('agent.memory_tombstones'::regclass),('agent.memory_index_projections'::regclass),
      ('agent.retrieval_manifests'::regclass),('agent.retrieval_manifest_chunks'::regclass),
      ('agent.memory_accesses'::regclass)
    ) AS expected(table_oid)
    WHERE NOT EXISTS (SELECT 1 FROM pg_class c WHERE c.oid=expected.table_oid AND c.relrowsecurity AND c.relforcerowsecurity)
  ) THEN RAISE EXCEPTION 'memory or retrieval RLS boundary is absent'; END IF;
END
$verify$;
ROLLBACK;
SELECT json_build_object(
  'status','passed','schema_version',28,
  'memory','event_sourced_revision_lineage_tool_guard_retrieval_manifest'
) AS current_schema_verification;
