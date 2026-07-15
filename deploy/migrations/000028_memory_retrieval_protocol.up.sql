DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM agent.memory_documents) THEN
    RAISE EXCEPTION 'memory protocol backfill required before migration 28' USING ERRCODE='55000';
  END IF;
END
$$;

ALTER TABLE agent.tool_calls
  ADD COLUMN execution_mode text NOT NULL DEFAULT 'worker_runtime',
  ADD CONSTRAINT tool_calls_execution_mode_contract CHECK (
    execution_mode IN ('worker_runtime','inline_platform')
    AND (execution_mode<>'inline_platform' OR (tool_name='memory_write' AND effect_class='idempotent_write'))
  );

ALTER TABLE agent.llm_attempts
  ADD CONSTRAINT llm_attempts_context_schema_contract CHECK (
    (context_manifest->>'schema_version'='1' AND NOT (context_manifest ? 'retrieval'))
    OR (context_manifest->>'schema_version'='2' AND jsonb_typeof(context_manifest->'retrieval')='object'
      AND NULLIF(context_manifest->'retrieval'->>'manifest_id','') IS NOT NULL)
  );

ALTER TABLE agent.memory_documents
  DROP CONSTRAINT IF EXISTS memory_documents_tenant_id_id_revision_key,
  DROP COLUMN memory_kind,
  DROP COLUMN source_event_id,
  DROP COLUMN revision,
  DROP COLUMN payload_ref,
  DROP COLUMN content_hash,
  DROP COLUMN embedding_ref,
  ADD COLUMN scope_kind text,
  ADD COLUMN scope_id uuid,
  ADD COLUMN current_revision bigint,
  ADD COLUMN superseded_by uuid,
  ADD COLUMN deleted_at timestamptz,
  ADD COLUMN upserted_event_id uuid,
  ADD COLUMN deleted_event_id uuid;

ALTER TABLE agent.memory_documents
  ALTER COLUMN scope_kind SET NOT NULL,
  ALTER COLUMN scope_id SET NOT NULL,
  ALTER COLUMN current_revision SET NOT NULL,
  ALTER COLUMN upserted_event_id SET NOT NULL,
  ADD CONSTRAINT memory_documents_tenant_id_id_unique UNIQUE (tenant_id,id),
  ADD CONSTRAINT memory_documents_tenant_id_id_version_unique UNIQUE (tenant_id,id,version),
  ADD CONSTRAINT memory_documents_scope_contract CHECK (
    scope_kind IN ('conversation','user','project','team') AND version>0
    AND current_revision>0 AND current_revision<=version
    AND status IN ('active','deleted')
  ),
  ADD CONSTRAINT memory_documents_lifecycle_contract CHECK (
    (status='active' AND version=current_revision AND deleted_at IS NULL AND deleted_event_id IS NULL)
    OR (status='deleted' AND version=current_revision+1 AND deleted_at IS NOT NULL AND deleted_event_id IS NOT NULL)
  ),
  ADD CONSTRAINT memory_documents_upserted_event_fk FOREIGN KEY (upserted_event_id)
    REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  ADD CONSTRAINT memory_documents_deleted_event_fk FOREIGN KEY (deleted_event_id)
    REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  ADD CONSTRAINT memory_documents_superseded_by_fk FOREIGN KEY (tenant_id,superseded_by)
    REFERENCES agent.memory_documents(tenant_id,id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;

CREATE TABLE agent.memory_document_revisions (
  tenant_id uuid NOT NULL,
  memory_id uuid NOT NULL,
  memory_version bigint NOT NULL,
  user_id uuid NOT NULL,
  scope_kind text NOT NULL,
  scope_id uuid NOT NULL,
  memory_kind text NOT NULL,
  content_ref text NOT NULL,
  content_hmac bytea NOT NULL,
  content_type text NOT NULL,
  summary_ref text,
  sensitivity_labels jsonb NOT NULL,
  source_kind text NOT NULL,
  trust_label text NOT NULL,
  derivation_kind text NOT NULL,
  confidence numeric(5,4) NOT NULL,
  encryption_subject_id uuid NOT NULL,
  key_ref text NOT NULL,
  embedding_model_id text NOT NULL,
  tags jsonb NOT NULL,
  tool_call_id uuid NOT NULL,
  tool_call_version bigint NOT NULL,
  policy_version bigint NOT NULL,
  guardrail_snapshot_id text NOT NULL,
  index_generation bigint NOT NULL,
  expires_at timestamptz,
  supersedes_memory_id uuid,
  upserted_event_id uuid NOT NULL,
  created_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id,memory_id,memory_version),
  CONSTRAINT memory_revision_scope_contract CHECK (
    memory_version>0 AND scope_kind IN ('conversation','user','project','team')
    AND memory_kind IN ('preference','goal','capability_context','learning_history')
    AND NULLIF(content_ref,'') IS NOT NULL AND octet_length(content_hmac)=32
    AND content_type IN ('text','structured','code_snippet')
    AND jsonb_typeof(sensitivity_labels)='array' AND jsonb_typeof(tags)='array'
    AND source_kind IN ('agent_extracted','user_stated','system_derived')
    AND trust_label IN ('user_asserted','derived','untrusted_external')
    AND ((source_kind='user_stated' AND trust_label='user_asserted')
      OR (source_kind<>'user_stated' AND trust_label IN ('derived','untrusted_external')))
    AND derivation_kind IN ('direct','summarized','merged','inferred')
    AND confidence>=0 AND confidence<=1 AND NULLIF(key_ref,'') IS NOT NULL
    AND NULLIF(embedding_model_id,'') IS NOT NULL AND tool_call_version>0
    AND policy_version>0 AND NULLIF(guardrail_snapshot_id,'') IS NOT NULL AND index_generation>0
  ),
  CONSTRAINT memory_revision_document_fk FOREIGN KEY (tenant_id,memory_id)
    REFERENCES agent.memory_documents(tenant_id,id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  CONSTRAINT memory_revision_tool_fk FOREIGN KEY (tenant_id,tool_call_id)
    REFERENCES agent.tool_calls(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT memory_revision_upserted_event_fk FOREIGN KEY (upserted_event_id)
    REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  CONSTRAINT memory_revision_supersedes_fk FOREIGN KEY (tenant_id,supersedes_memory_id)
    REFERENCES agent.memory_documents(tenant_id,id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED
);

CREATE TABLE agent.memory_revision_sources (
  tenant_id uuid NOT NULL,
  memory_id uuid NOT NULL,
  memory_version bigint NOT NULL,
  source_ordinal integer NOT NULL,
  source_kind text NOT NULL,
  source_ref text NOT NULL,
  source_version bigint,
  created_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id,memory_id,memory_version,source_ordinal),
  CONSTRAINT memory_revision_sources_scope_contract CHECK (
    source_ordinal>0 AND source_kind IN ('event','run','payload','memory','user_statement','system_rule')
    AND NULLIF(source_ref,'') IS NOT NULL AND (source_version IS NULL OR source_version>0)
  ),
  CONSTRAINT memory_revision_sources_revision_fk FOREIGN KEY (tenant_id,memory_id,memory_version)
    REFERENCES agent.memory_document_revisions(tenant_id,memory_id,memory_version) ON DELETE RESTRICT
);

CREATE TABLE agent.memory_revision_subjects (
  tenant_id uuid NOT NULL,
  memory_id uuid NOT NULL,
  memory_version bigint NOT NULL,
  data_subject_id uuid NOT NULL,
  created_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id,memory_id,memory_version,data_subject_id),
  CONSTRAINT memory_revision_subjects_revision_fk FOREIGN KEY (tenant_id,memory_id,memory_version)
    REFERENCES agent.memory_document_revisions(tenant_id,memory_id,memory_version) ON DELETE RESTRICT
);

CREATE TABLE agent.memory_revision_derivations (
  tenant_id uuid NOT NULL,
  memory_id uuid NOT NULL,
  memory_version bigint NOT NULL,
  source_memory_id uuid NOT NULL,
  source_memory_version bigint NOT NULL,
  created_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id,memory_id,memory_version,source_memory_id,source_memory_version),
  CONSTRAINT memory_revision_derivations_no_self CHECK (
    memory_id<>source_memory_id OR memory_version<>source_memory_version
  ),
  CONSTRAINT memory_revision_derivations_target_fk FOREIGN KEY (tenant_id,memory_id,memory_version)
    REFERENCES agent.memory_document_revisions(tenant_id,memory_id,memory_version) ON DELETE RESTRICT,
  CONSTRAINT memory_revision_derivations_source_fk FOREIGN KEY (tenant_id,source_memory_id,source_memory_version)
    REFERENCES agent.memory_document_revisions(tenant_id,memory_id,memory_version) ON DELETE RESTRICT
);

ALTER TABLE agent.memory_documents
  ADD CONSTRAINT memory_documents_current_revision_fk FOREIGN KEY (tenant_id,id,current_revision)
    REFERENCES agent.memory_document_revisions(tenant_id,memory_id,memory_version)
    ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;

CREATE TABLE agent.memory_tombstones (
  tenant_id uuid NOT NULL,
  memory_id uuid NOT NULL,
  memory_version bigint NOT NULL,
  reason text NOT NULL,
  erased_subject_ids jsonb NOT NULL,
  purge_command_id uuid NOT NULL,
  deleted_event_id uuid NOT NULL,
  deleted_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id,memory_id,memory_version),
  CONSTRAINT memory_tombstones_reason_contract CHECK (
    reason IN ('user_request','expiration','superseded','erasure')
    AND jsonb_typeof(erased_subject_ids)='array'
  ),
  CONSTRAINT memory_tombstones_document_fk FOREIGN KEY (tenant_id,memory_id,memory_version)
    REFERENCES agent.memory_documents(tenant_id,id,version) ON DELETE RESTRICT,
  CONSTRAINT memory_tombstones_event_fk FOREIGN KEY (deleted_event_id)
    REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED
);

CREATE TABLE agent.memory_index_projections (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  memory_id uuid NOT NULL,
  memory_version bigint NOT NULL,
  index_generation bigint NOT NULL,
  embedding_model_id text NOT NULL,
  embedding_model_version text NOT NULL,
  vector_dimensions integer NOT NULL,
  embedding_ref text,
  lexical_ref text,
  status text NOT NULL,
  indexed_event_id uuid,
  purge_event_id uuid,
  purge_receipt_ref text,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  CONSTRAINT memory_index_projections_tenant_id_id_unique UNIQUE (tenant_id,id),
  CONSTRAINT memory_index_projections_revision_generation_unique UNIQUE (tenant_id,memory_id,memory_version,index_generation),
  CONSTRAINT memory_index_projection_scope_contract CHECK (
    index_generation>0 AND NULLIF(embedding_model_id,'') IS NOT NULL
    AND NULLIF(embedding_model_version,'') IS NOT NULL AND vector_dimensions>0
    AND status IN ('pending','indexed','purge_pending','purged')
  ),
  CONSTRAINT memory_index_projection_lifecycle_contract CHECK (
    (status='pending' AND embedding_ref IS NULL AND lexical_ref IS NULL AND indexed_event_id IS NULL AND purge_event_id IS NULL AND purge_receipt_ref IS NULL)
    OR (status='indexed' AND NULLIF(embedding_ref,'') IS NOT NULL AND NULLIF(lexical_ref,'') IS NOT NULL AND indexed_event_id IS NOT NULL AND purge_event_id IS NULL AND purge_receipt_ref IS NULL)
    OR (status='purge_pending' AND indexed_event_id IS NOT NULL AND purge_event_id IS NULL AND purge_receipt_ref IS NULL)
    OR (status='purged' AND embedding_ref IS NULL AND lexical_ref IS NULL AND indexed_event_id IS NOT NULL AND purge_event_id IS NOT NULL AND NULLIF(purge_receipt_ref,'') IS NOT NULL)
  ),
  CONSTRAINT memory_index_projection_revision_fk FOREIGN KEY (tenant_id,memory_id,memory_version)
    REFERENCES agent.memory_document_revisions(tenant_id,memory_id,memory_version) ON DELETE RESTRICT,
  CONSTRAINT memory_index_projection_indexed_event_fk FOREIGN KEY (indexed_event_id)
    REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  CONSTRAINT memory_index_projection_purge_event_fk FOREIGN KEY (purge_event_id)
    REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED
);

CREATE TABLE agent.retrieval_manifests (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  llm_attempt_id uuid NOT NULL,
  context_manifest_hash text NOT NULL,
  query_hmac bytea NOT NULL,
  retrieval_model_id text NOT NULL,
  retrieval_model_version text NOT NULL,
  policy_snapshot_id text NOT NULL,
  guardrail_snapshot_id text NOT NULL,
  index_generation bigint NOT NULL,
  token_budget bigint NOT NULL,
  committed_event_id uuid NOT NULL,
  committed_at timestamptz NOT NULL,
  CONSTRAINT retrieval_manifests_tenant_id_id_unique UNIQUE (tenant_id,id),
  CONSTRAINT retrieval_manifests_attempt_unique UNIQUE (tenant_id,llm_attempt_id),
  CONSTRAINT retrieval_manifests_scope_contract CHECK (
    NULLIF(context_manifest_hash,'') IS NOT NULL AND octet_length(query_hmac)=32
    AND NULLIF(retrieval_model_id,'') IS NOT NULL AND NULLIF(retrieval_model_version,'') IS NOT NULL
    AND NULLIF(policy_snapshot_id,'') IS NOT NULL AND NULLIF(guardrail_snapshot_id,'') IS NOT NULL
    AND index_generation>0 AND token_budget>=0
  ),
  CONSTRAINT retrieval_manifests_tenant_fk FOREIGN KEY (tenant_id)
    REFERENCES identity.tenants(id) ON DELETE RESTRICT,
  CONSTRAINT retrieval_manifests_llm_attempt_fk FOREIGN KEY (tenant_id,llm_attempt_id)
    REFERENCES agent.llm_attempts(tenant_id,id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  CONSTRAINT retrieval_manifests_event_fk FOREIGN KEY (committed_event_id)
    REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED
);

CREATE TABLE agent.retrieval_manifest_chunks (
  tenant_id uuid NOT NULL,
  manifest_id uuid NOT NULL,
  ordinal integer NOT NULL,
  memory_id uuid NOT NULL,
  memory_version bigint NOT NULL,
  index_generation bigint NOT NULL,
  chunk_id text NOT NULL,
  content_ref text NOT NULL,
  content_hmac bytea NOT NULL,
  score numeric(9,8) NOT NULL,
  scope_kind text NOT NULL,
  source_label text NOT NULL,
  trust_label text NOT NULL,
  PRIMARY KEY (tenant_id,manifest_id,ordinal),
  CONSTRAINT retrieval_manifest_chunks_unique UNIQUE (tenant_id,manifest_id,memory_id,memory_version,chunk_id),
  CONSTRAINT retrieval_manifest_chunks_scope_contract CHECK (
    ordinal>0 AND index_generation>0 AND NULLIF(chunk_id,'') IS NOT NULL
    AND NULLIF(content_ref,'') IS NOT NULL AND octet_length(content_hmac)=32
    AND score>=0 AND score<=1 AND scope_kind IN ('conversation','user','project','team')
    AND source_label='retrieved_memory' AND trust_label IN ('user_asserted','derived','untrusted_external')
  ),
  CONSTRAINT retrieval_manifest_chunks_manifest_fk FOREIGN KEY (tenant_id,manifest_id)
    REFERENCES agent.retrieval_manifests(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT retrieval_manifest_chunks_revision_fk FOREIGN KEY (tenant_id,memory_id,memory_version)
    REFERENCES agent.memory_document_revisions(tenant_id,memory_id,memory_version) ON DELETE RESTRICT,
  CONSTRAINT retrieval_manifest_chunks_projection_fk FOREIGN KEY (tenant_id,memory_id,memory_version,index_generation)
    REFERENCES agent.memory_index_projections(tenant_id,memory_id,memory_version,index_generation) ON DELETE RESTRICT
);

CREATE TABLE agent.memory_accesses (
  tenant_id uuid NOT NULL,
  manifest_id uuid NOT NULL,
  memory_id uuid NOT NULL,
  memory_version bigint NOT NULL,
  chunk_id text NOT NULL,
  accessed_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id,manifest_id,memory_id,memory_version,chunk_id),
  CONSTRAINT memory_accesses_chunk_fk FOREIGN KEY (tenant_id,manifest_id,memory_id,memory_version,chunk_id)
    REFERENCES agent.retrieval_manifest_chunks(tenant_id,manifest_id,memory_id,memory_version,chunk_id) ON DELETE RESTRICT
);

CREATE FUNCTION agent.enforce_memory_document_lifecycle() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'memory document deletion is forbidden'; END IF;
  IF OLD.status<>'active' THEN RAISE EXCEPTION 'terminal memory document mutation is forbidden'; END IF;
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.user_id IS DISTINCT FROM OLD.user_id OR NEW.created_at IS DISTINCT FROM OLD.created_at
    OR NEW.scope_kind IS DISTINCT FROM OLD.scope_kind OR NEW.scope_id IS DISTINCT FROM OLD.scope_id THEN
    RAISE EXCEPTION 'immutable memory scope mutation is forbidden';
  END IF;
  IF NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at THEN
    RAISE EXCEPTION 'memory document version must advance exactly once';
  END IF;
  IF NEW.status='active' AND NEW.current_revision=NEW.version AND NEW.upserted_event_id IS DISTINCT FROM OLD.upserted_event_id
    AND NEW.deleted_at IS NULL AND NEW.deleted_event_id IS NULL THEN RETURN NEW; END IF;
  IF NEW.status='deleted' AND NEW.current_revision=OLD.current_revision AND NEW.upserted_event_id=OLD.upserted_event_id
    AND NEW.deleted_at IS NOT NULL AND NEW.deleted_event_id IS NOT NULL THEN RETURN NEW; END IF;
  RAISE EXCEPTION 'invalid memory document transition';
END
$$;

CREATE TRIGGER memory_documents_lifecycle BEFORE UPDATE OR DELETE ON agent.memory_documents
  FOR EACH ROW EXECUTE FUNCTION agent.enforce_memory_document_lifecycle();

CREATE TRIGGER memory_document_revisions_append_only BEFORE UPDATE OR DELETE ON agent.memory_document_revisions
  FOR EACH ROW EXECUTE FUNCTION agent.reject_append_only_mutation();
CREATE TRIGGER memory_revision_sources_append_only BEFORE UPDATE OR DELETE ON agent.memory_revision_sources
  FOR EACH ROW EXECUTE FUNCTION agent.reject_append_only_mutation();
CREATE TRIGGER memory_revision_subjects_append_only BEFORE UPDATE OR DELETE ON agent.memory_revision_subjects
  FOR EACH ROW EXECUTE FUNCTION agent.reject_append_only_mutation();
CREATE TRIGGER memory_revision_derivations_append_only BEFORE UPDATE OR DELETE ON agent.memory_revision_derivations
  FOR EACH ROW EXECUTE FUNCTION agent.reject_append_only_mutation();
CREATE TRIGGER memory_tombstones_append_only BEFORE UPDATE OR DELETE ON agent.memory_tombstones
  FOR EACH ROW EXECUTE FUNCTION agent.reject_append_only_mutation();
CREATE TRIGGER retrieval_manifests_append_only BEFORE UPDATE OR DELETE ON agent.retrieval_manifests
  FOR EACH ROW EXECUTE FUNCTION agent.reject_append_only_mutation();
CREATE TRIGGER retrieval_manifest_chunks_append_only BEFORE UPDATE OR DELETE ON agent.retrieval_manifest_chunks
  FOR EACH ROW EXECUTE FUNCTION agent.reject_append_only_mutation();
CREATE TRIGGER memory_accesses_append_only BEFORE UPDATE OR DELETE ON agent.memory_accesses
  FOR EACH ROW EXECUTE FUNCTION agent.reject_append_only_mutation();

CREATE FUNCTION agent.validate_memory_revision_commit() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM agent.memory_documents d
    JOIN agent.tool_calls t ON t.tenant_id=d.tenant_id AND t.id=NEW.tool_call_id
    JOIN agent.events te ON te.tenant_id=t.tenant_id AND te.id=t.result_event_id
    JOIN agent.events me ON me.tenant_id=d.tenant_id AND me.id=NEW.upserted_event_id
    JOIN product.memory_policies p ON p.tenant_id=d.tenant_id AND p.user_id=d.user_id
    WHERE d.tenant_id=NEW.tenant_id AND d.id=NEW.memory_id AND d.current_revision=NEW.memory_version
      AND d.upserted_event_id=NEW.upserted_event_id AND d.user_id=NEW.user_id
      AND d.scope_kind=NEW.scope_kind AND d.scope_id=NEW.scope_id AND d.status='active'
      AND t.user_id=NEW.user_id AND t.tool_name='memory_write' AND t.execution_mode='inline_platform'
      AND t.status='succeeded'
      AND t.tool_call_version=NEW.tool_call_version AND t.effect_class='idempotent_write'
      AND te.aggregate_kind='tool_call' AND te.aggregate_id=t.id AND te.aggregate_version=t.tool_call_version
      AND te.event_type='ToolCallSucceeded'
      AND me.aggregate_kind='memory_document' AND me.aggregate_id=NEW.memory_id
      AND me.aggregate_version=NEW.memory_version AND me.event_type='MemoryUpserted'
      AND me.event_schema_version=2 AND me.causation_id=te.id
      AND p.enabled AND p.version=NEW.policy_version AND p.allowed_kinds ? NEW.memory_kind
  ) THEN RAISE EXCEPTION 'memory revision lacks governed memory_write fact'; END IF;
  IF NOT EXISTS (SELECT 1 FROM agent.memory_revision_sources s WHERE s.tenant_id=NEW.tenant_id AND s.memory_id=NEW.memory_id AND s.memory_version=NEW.memory_version)
    OR NOT EXISTS (SELECT 1 FROM agent.memory_revision_subjects s WHERE s.tenant_id=NEW.tenant_id AND s.memory_id=NEW.memory_id AND s.memory_version=NEW.memory_version) THEN
    RAISE EXCEPTION 'memory revision requires source and data subject lineage';
  END IF;
  RETURN NULL;
END
$$;

CREATE CONSTRAINT TRIGGER memory_revision_commit_guard
  AFTER INSERT ON agent.memory_document_revisions DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION agent.validate_memory_revision_commit();

CREATE FUNCTION agent.validate_memory_tombstone_commit() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM agent.memory_documents d JOIN agent.events e ON e.tenant_id=d.tenant_id AND e.id=NEW.deleted_event_id
    WHERE d.tenant_id=NEW.tenant_id AND d.id=NEW.memory_id AND d.version=NEW.memory_version
      AND d.status='deleted' AND d.deleted_event_id=NEW.deleted_event_id AND d.deleted_at=NEW.deleted_at
      AND e.aggregate_kind='memory_document' AND e.aggregate_id=d.id AND e.aggregate_version=d.version
      AND e.event_type='MemoryDeleted' AND e.event_schema_version=2
  ) THEN RAISE EXCEPTION 'memory tombstone lacks exact deletion fact'; END IF;
  RETURN NULL;
END
$$;

CREATE CONSTRAINT TRIGGER memory_tombstone_commit_guard
  AFTER INSERT ON agent.memory_tombstones DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION agent.validate_memory_tombstone_commit();

CREATE FUNCTION agent.validate_retrieval_manifest_commit() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM agent.llm_attempts l JOIN agent.events e ON e.tenant_id=l.tenant_id AND e.id=NEW.committed_event_id
    WHERE l.tenant_id=NEW.tenant_id AND l.id=NEW.llm_attempt_id AND l.user_id=NEW.user_id
      AND l.context_manifest_hash=NEW.context_manifest_hash
      AND l.context_manifest->>'schema_version'='2'
      AND l.context_manifest->'retrieval'->>'manifest_id'=NEW.id::text
      AND jsonb_array_length(l.context_manifest->'memory')=(
        SELECT count(DISTINCT (c.memory_id,c.memory_version)) FROM agent.retrieval_manifest_chunks c
        WHERE c.tenant_id=NEW.tenant_id AND c.manifest_id=NEW.id
      )
      AND NOT EXISTS (
        SELECT 1 FROM agent.retrieval_manifest_chunks c
        JOIN agent.memory_document_revisions r ON r.tenant_id=c.tenant_id AND r.memory_id=c.memory_id AND r.memory_version=c.memory_version
        WHERE c.tenant_id=NEW.tenant_id AND c.manifest_id=NEW.id AND NOT EXISTS (
          SELECT 1 FROM jsonb_array_elements(l.context_manifest->'memory') item
          WHERE item->>'id'=c.memory_id::text AND item->>'version'=c.memory_version::text
            AND item->>'hash'=encode(r.content_hmac,'hex')
        )
      )
      AND e.aggregate_kind='retrieval_manifest' AND e.aggregate_id=NEW.id
      AND e.aggregate_version=1 AND e.event_type='RetrievalManifestCommitted' AND e.event_schema_version=1
  ) THEN RAISE EXCEPTION 'retrieval manifest is not bound to exact LLM context'; END IF;
  IF EXISTS (
    SELECT 1 FROM agent.retrieval_manifest_chunks c
    LEFT JOIN agent.memory_documents d ON d.tenant_id=c.tenant_id AND d.id=c.memory_id
    LEFT JOIN agent.memory_index_projections p ON p.tenant_id=c.tenant_id AND p.memory_id=c.memory_id
      AND p.memory_version=c.memory_version AND p.index_generation=c.index_generation
    LEFT JOIN agent.llm_attempts l ON l.tenant_id=NEW.tenant_id AND l.id=NEW.llm_attempt_id
    LEFT JOIN agent.runs r ON r.tenant_id=l.tenant_id AND r.id=l.run_id
    WHERE c.tenant_id=NEW.tenant_id AND c.manifest_id=NEW.id
      AND (d.id IS NULL OR d.status<>'active' OR d.current_revision<>c.memory_version OR p.status<>'indexed'
        OR (d.scope_kind='conversation' AND d.scope_id<>r.conversation_id)
        OR (d.scope_kind='user' AND d.scope_id<>NEW.user_id)
        OR (d.scope_kind='project' AND NOT EXISTS (
          SELECT 1 FROM product.projects pr WHERE pr.tenant_id=d.tenant_id AND pr.id=d.scope_id
            AND (pr.user_id=NEW.user_id OR EXISTS (
              SELECT 1 FROM product.share_grants g WHERE g.tenant_id=pr.tenant_id
                AND g.resource_kind='project' AND g.resource_id=pr.id AND g.grantee_user_id=NEW.user_id
                AND g.revoked_at IS NULL AND (g.expires_at IS NULL OR g.expires_at>NEW.committed_at)
            ))
        ))
        OR (d.scope_kind='team' AND d.scope_id<>NEW.tenant_id))
  ) THEN RAISE EXCEPTION 'retrieval manifest contains inaccessible or stale memory'; END IF;
  RETURN NULL;
END
$$;

CREATE CONSTRAINT TRIGGER retrieval_manifest_commit_guard
  AFTER INSERT ON agent.retrieval_manifests DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION agent.validate_retrieval_manifest_commit();

CREATE FUNCTION agent.validate_llm_retrieval_binding() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.context_manifest->>'schema_version'='2' AND NOT EXISTS (
    SELECT 1 FROM agent.retrieval_manifests m
    WHERE m.tenant_id=NEW.tenant_id AND m.id=(NEW.context_manifest->'retrieval'->>'manifest_id')::uuid
      AND m.llm_attempt_id=NEW.id AND m.user_id=NEW.user_id
      AND m.context_manifest_hash=NEW.context_manifest_hash
  ) THEN RAISE EXCEPTION 'LLM context lacks its exact retrieval manifest'; END IF;
  RETURN NULL;
END
$$;

CREATE CONSTRAINT TRIGGER llm_retrieval_binding_guard
  AFTER INSERT ON agent.llm_attempts DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION agent.validate_llm_retrieval_binding();

CREATE FUNCTION agent.enforce_memory_index_projection_lifecycle() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'memory index projection deletion is forbidden'; END IF;
  IF NEW.tenant_id IS DISTINCT FROM OLD.tenant_id OR NEW.memory_id IS DISTINCT FROM OLD.memory_id
    OR NEW.memory_version IS DISTINCT FROM OLD.memory_version OR NEW.index_generation IS DISTINCT FROM OLD.index_generation
    OR NEW.embedding_model_id IS DISTINCT FROM OLD.embedding_model_id
    OR NEW.embedding_model_version IS DISTINCT FROM OLD.embedding_model_version
    OR NEW.vector_dimensions IS DISTINCT FROM OLD.vector_dimensions OR NEW.created_at IS DISTINCT FROM OLD.created_at
    OR NEW.updated_at<OLD.updated_at THEN RAISE EXCEPTION 'immutable memory index scope mutation is forbidden'; END IF;
  IF OLD.status='pending' AND NEW.status='indexed' THEN RETURN NEW; END IF;
  IF OLD.status='indexed' AND NEW.status='purge_pending' THEN RETURN NEW; END IF;
  IF OLD.status='purge_pending' AND NEW.status='purged' THEN RETURN NEW; END IF;
  RAISE EXCEPTION 'invalid memory index projection transition';
END
$$;

CREATE TRIGGER memory_index_projections_lifecycle BEFORE UPDATE OR DELETE ON agent.memory_index_projections
  FOR EACH ROW EXECUTE FUNCTION agent.enforce_memory_index_projection_lifecycle();

CREATE FUNCTION agent.validate_memory_index_projection_commit() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.status='pending' AND NEW.status='indexed' AND NOT EXISTS (
    SELECT 1 FROM agent.events e
    WHERE e.tenant_id=NEW.tenant_id AND e.id=NEW.indexed_event_id
      AND e.aggregate_kind='memory_index_projection' AND e.aggregate_id=NEW.id
      AND e.aggregate_version=1 AND e.event_type='MemoryIndexProjectionIndexed' AND e.event_schema_version=1
  ) THEN RAISE EXCEPTION 'indexed memory projection lacks exact completion fact'; END IF;
  RETURN NULL;
END
$$;

CREATE CONSTRAINT TRIGGER memory_index_projection_commit_guard
  AFTER UPDATE ON agent.memory_index_projections DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION agent.validate_memory_index_projection_commit();

CREATE INDEX memory_documents_scope_active_idx ON agent.memory_documents(tenant_id,user_id,scope_kind,scope_id,id)
  WHERE status='active';
CREATE INDEX memory_documents_active_expiration_scan_idx ON agent.memory_documents(tenant_id,expires_at,id)
  WHERE status='active' AND expires_at IS NOT NULL;
CREATE INDEX memory_revision_subjects_erasure_idx ON agent.memory_revision_subjects(tenant_id,data_subject_id,memory_id,memory_version);
CREATE INDEX memory_revision_derivations_reverse_idx ON agent.memory_revision_derivations(tenant_id,source_memory_id,source_memory_version,memory_id,memory_version);
CREATE INDEX memory_index_projection_pending_idx ON agent.memory_index_projections(tenant_id,status,index_generation,memory_id,memory_version)
  WHERE status IN ('pending','purge_pending');

ALTER TABLE agent.memory_document_revisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent.memory_document_revisions FORCE ROW LEVEL SECURITY;
CREATE POLICY memory_document_revisions_tenant_isolation ON agent.memory_document_revisions
  USING (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid);
ALTER TABLE agent.memory_revision_sources ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent.memory_revision_sources FORCE ROW LEVEL SECURITY;
CREATE POLICY memory_revision_sources_tenant_isolation ON agent.memory_revision_sources
  USING (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid);
ALTER TABLE agent.memory_revision_subjects ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent.memory_revision_subjects FORCE ROW LEVEL SECURITY;
CREATE POLICY memory_revision_subjects_tenant_isolation ON agent.memory_revision_subjects
  USING (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid);
ALTER TABLE agent.memory_revision_derivations ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent.memory_revision_derivations FORCE ROW LEVEL SECURITY;
CREATE POLICY memory_revision_derivations_tenant_isolation ON agent.memory_revision_derivations
  USING (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid);
ALTER TABLE agent.memory_tombstones ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent.memory_tombstones FORCE ROW LEVEL SECURITY;
CREATE POLICY memory_tombstones_tenant_isolation ON agent.memory_tombstones
  USING (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid);
ALTER TABLE agent.memory_index_projections ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent.memory_index_projections FORCE ROW LEVEL SECURITY;
CREATE POLICY memory_index_projections_tenant_isolation ON agent.memory_index_projections
  USING (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid);
ALTER TABLE agent.retrieval_manifests ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent.retrieval_manifests FORCE ROW LEVEL SECURITY;
CREATE POLICY retrieval_manifests_tenant_isolation ON agent.retrieval_manifests
  USING (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid);
ALTER TABLE agent.retrieval_manifest_chunks ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent.retrieval_manifest_chunks FORCE ROW LEVEL SECURITY;
CREATE POLICY retrieval_manifest_chunks_tenant_isolation ON agent.retrieval_manifest_chunks
  USING (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid);
ALTER TABLE agent.memory_accesses ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent.memory_accesses FORCE ROW LEVEL SECURITY;
CREATE POLICY memory_accesses_tenant_isolation ON agent.memory_accesses
  USING (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid);
