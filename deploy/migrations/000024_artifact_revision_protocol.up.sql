DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM product.artifacts) OR EXISTS (SELECT 1 FROM product.artifact_revisions) THEN
    RAISE EXCEPTION 'artifact revision protocol backfill required before migration 24' USING ERRCODE='55000';
  END IF;
END
$$;

ALTER TABLE product.evidence
  ADD CONSTRAINT evidence_tenant_id_id_unique UNIQUE (tenant_id,id),
  ADD CONSTRAINT evidence_immutable_binding_unique UNIQUE (tenant_id,id,version,content_hash);

ALTER TABLE product.projects
  ADD CONSTRAINT projects_tenant_id_id_unique UNIQUE (tenant_id,id);

ALTER TABLE product.artifacts
  ADD COLUMN title text,
  ADD COLUMN current_revision_id uuid,
  ADD COLUMN current_content_hash text,
  ADD COLUMN archived_at timestamptz;

ALTER TABLE product.artifacts
  ALTER COLUMN title SET NOT NULL,
  ADD CONSTRAINT artifacts_tenant_id_id_unique UNIQUE (tenant_id,id),
  ADD CONSTRAINT artifacts_status_contract CHECK (status IN ('draft','ready','archived')),
  ADD CONSTRAINT artifacts_scope_contract CHECK (
    NULLIF(artifact_kind,'') IS NOT NULL AND length(title) BETWEEN 1 AND 200 AND current_revision>=0
  ),
  ADD CONSTRAINT artifacts_current_revision_contract CHECK (
    (status='draft' AND current_revision=0 AND current_revision_id IS NULL AND current_content_hash IS NULL AND archived_at IS NULL)
    OR (status='ready' AND current_revision>0 AND current_revision_id IS NOT NULL AND NULLIF(current_content_hash,'') IS NOT NULL AND archived_at IS NULL)
    OR (status='archived' AND archived_at IS NOT NULL AND (
      (current_revision=0 AND current_revision_id IS NULL AND current_content_hash IS NULL)
      OR (current_revision>0 AND current_revision_id IS NOT NULL AND NULLIF(current_content_hash,'') IS NOT NULL)
    ))
  );

ALTER TABLE product.artifact_revisions
  ADD COLUMN project_id uuid,
  ADD COLUMN workspace_revision text,
  ADD COLUMN object_version text,
  ADD COLUMN media_type text,
  ADD COLUMN byte_size bigint,
  ADD COLUMN evidence_manifest_hash text,
  ADD COLUMN scan_result_hash text,
  ADD COLUMN created_event_id uuid;

ALTER TABLE product.artifact_revisions
  ALTER COLUMN project_id SET NOT NULL,
  ALTER COLUMN workspace_revision SET NOT NULL,
  ALTER COLUMN object_version SET NOT NULL,
  ALTER COLUMN media_type SET NOT NULL,
  ALTER COLUMN byte_size SET NOT NULL,
  ALTER COLUMN evidence_manifest_hash SET NOT NULL,
  ALTER COLUMN scan_result_hash SET NOT NULL,
  ALTER COLUMN created_event_id SET NOT NULL,
  ADD CONSTRAINT artifact_revisions_tenant_id_id_unique UNIQUE (tenant_id,id),
  ADD CONSTRAINT artifact_revisions_content_unique UNIQUE (tenant_id,artifact_id,content_hash),
  ADD CONSTRAINT artifact_revisions_scope_contract CHECK (
    revision>0 AND NULLIF(content_hash,'') IS NOT NULL AND NULLIF(object_ref,'') IS NOT NULL
    AND NULLIF(object_version,'') IS NOT NULL AND NULLIF(workspace_revision,'') IS NOT NULL
    AND NULLIF(media_type,'') IS NOT NULL AND byte_size>0 AND byte_size<=1073741824
    AND scan_status='passed' AND NULLIF(scan_result_hash,'') IS NOT NULL
    AND jsonb_typeof(evidence_manifest)='object' AND NULLIF(evidence_manifest_hash,'') IS NOT NULL
  ),
  ADD CONSTRAINT artifact_revisions_tenant_artifact_fk FOREIGN KEY (tenant_id,artifact_id)
    REFERENCES product.artifacts(tenant_id,id) ON DELETE RESTRICT,
  ADD CONSTRAINT artifact_revisions_tenant_project_fk FOREIGN KEY (tenant_id,project_id)
    REFERENCES product.projects(tenant_id,id) ON DELETE RESTRICT,
  ADD CONSTRAINT artifact_revisions_created_event_fk FOREIGN KEY (created_event_id)
    REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE product.artifacts
  ADD CONSTRAINT artifacts_current_revision_fk FOREIGN KEY (tenant_id,current_revision_id)
    REFERENCES product.artifact_revisions(tenant_id,id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;

CREATE TABLE product.artifact_revision_evidence (
  tenant_id uuid NOT NULL,
  artifact_revision_id uuid NOT NULL,
  evidence_id uuid NOT NULL,
  evidence_version bigint NOT NULL,
  evidence_content_hash text NOT NULL,
  ordinal integer NOT NULL,
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (tenant_id,artifact_revision_id,evidence_id),
  CONSTRAINT artifact_revision_evidence_ordinal_unique UNIQUE (tenant_id,artifact_revision_id,ordinal),
  CONSTRAINT artifact_revision_evidence_scope_contract CHECK (
    evidence_version>0 AND ordinal>=0 AND NULLIF(evidence_content_hash,'') IS NOT NULL
  ),
  CONSTRAINT artifact_revision_evidence_revision_fk FOREIGN KEY (tenant_id,artifact_revision_id)
    REFERENCES product.artifact_revisions(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT artifact_revision_evidence_exact_fk FOREIGN KEY (tenant_id,evidence_id,evidence_version,evidence_content_hash)
    REFERENCES product.evidence(tenant_id,id,version,content_hash) ON DELETE RESTRICT
);

ALTER TABLE product.artifact_revision_evidence ENABLE ROW LEVEL SECURITY;
ALTER TABLE product.artifact_revision_evidence FORCE ROW LEVEL SECURITY;
CREATE POLICY artifact_revision_evidence_tenant_isolation ON product.artifact_revision_evidence
  USING (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid);
CREATE TRIGGER artifact_revision_evidence_append_only BEFORE UPDATE OR DELETE ON product.artifact_revision_evidence
  FOR EACH ROW EXECUTE FUNCTION agent.reject_append_only_mutation();

CREATE FUNCTION product.enforce_artifact_lifecycle() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'artifact deletion is forbidden'; END IF;
  IF OLD.status='archived' THEN RAISE EXCEPTION 'archived artifact mutation is forbidden'; END IF;
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.user_id IS DISTINCT FROM OLD.user_id OR NEW.project_id IS DISTINCT FROM OLD.project_id
    OR NEW.artifact_kind IS DISTINCT FROM OLD.artifact_kind OR NEW.title IS DISTINCT FROM OLD.title
    OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'immutable artifact scope mutation is forbidden';
  END IF;
  IF NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at THEN
    RAISE EXCEPTION 'artifact version must advance exactly once';
  END IF;
  IF OLD.status='draft' AND NEW.status='ready' AND OLD.current_revision=0 AND NEW.current_revision=1 THEN RETURN NEW; END IF;
  IF OLD.status='ready' AND NEW.status='ready' AND NEW.current_revision=OLD.current_revision+1
    AND NEW.current_revision_id IS DISTINCT FROM OLD.current_revision_id
    AND NEW.current_content_hash IS DISTINCT FROM OLD.current_content_hash THEN RETURN NEW; END IF;
  IF OLD.status IN ('draft','ready') AND NEW.status='archived' AND NEW.current_revision=OLD.current_revision
    AND NEW.current_revision_id IS NOT DISTINCT FROM OLD.current_revision_id
    AND NEW.current_content_hash IS NOT DISTINCT FROM OLD.current_content_hash THEN RETURN NEW; END IF;
  RAISE EXCEPTION 'invalid artifact transition % -> %',OLD.status,NEW.status;
END
$$;

CREATE TRIGGER artifacts_lifecycle BEFORE UPDATE OR DELETE ON product.artifacts
  FOR EACH ROW EXECUTE FUNCTION product.enforce_artifact_lifecycle();

CREATE INDEX artifact_revisions_manifest_idx ON product.artifact_revisions(tenant_id,artifact_id,revision);
CREATE INDEX artifact_revision_evidence_evidence_idx ON product.artifact_revision_evidence(tenant_id,evidence_id,artifact_revision_id);
