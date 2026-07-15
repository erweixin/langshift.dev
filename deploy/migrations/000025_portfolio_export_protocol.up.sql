DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM product.portfolio_exports) THEN
    RAISE EXCEPTION 'portfolio export protocol backfill required before migration 25' USING ERRCODE='55000';
  END IF;
END
$$;

ALTER TABLE product.project_workspace_bindings
  ADD CONSTRAINT project_workspace_bindings_tenant_id_id_unique UNIQUE (tenant_id,id),
  ADD CONSTRAINT project_workspace_bindings_exact_head_unique UNIQUE (tenant_id,id,version,head_revision,binding_manifest_hash);

ALTER TABLE product.artifact_revisions
  ADD CONSTRAINT artifact_revisions_exact_binding_unique UNIQUE (tenant_id,id,artifact_id,revision,content_hash);

ALTER TABLE product.portfolio_exports
  ADD COLUMN request_id text,
  ADD COLUMN project_version bigint,
  ADD COLUMN workspace_binding_id uuid,
  ADD COLUMN workspace_binding_version bigint,
  ADD COLUMN workspace_revision text,
  ADD COLUMN workspace_manifest_hash text,
  ADD COLUMN export_format text,
  ADD COLUMN revision_manifest_hash text,
  ADD COLUMN run_id uuid,
  ADD COLUMN start_command_id uuid,
  ADD COLUMN request_event_id uuid,
  ADD COLUMN completion_event_id uuid,
  ADD COLUMN object_version text,
  ADD COLUMN media_type text,
  ADD COLUMN byte_size bigint,
  ADD COLUMN scan_result_hash text,
  ADD COLUMN started_at timestamptz,
  ADD COLUMN failed_at timestamptz,
  ADD COLUMN failure_code text,
  ADD COLUMN expired_at timestamptz;

ALTER TABLE product.portfolio_exports
  ALTER COLUMN request_id SET NOT NULL,
  ALTER COLUMN project_version SET NOT NULL,
  ALTER COLUMN workspace_binding_id SET NOT NULL,
  ALTER COLUMN workspace_binding_version SET NOT NULL,
  ALTER COLUMN workspace_revision SET NOT NULL,
  ALTER COLUMN workspace_manifest_hash SET NOT NULL,
  ALTER COLUMN export_format SET NOT NULL,
  ALTER COLUMN revision_manifest_hash SET NOT NULL,
  ALTER COLUMN run_id SET NOT NULL,
  ALTER COLUMN start_command_id SET NOT NULL,
  ALTER COLUMN request_event_id SET NOT NULL,
  ADD CONSTRAINT portfolio_exports_tenant_id_id_unique UNIQUE (tenant_id,id),
  ADD CONSTRAINT portfolio_exports_request_unique UNIQUE (tenant_id,user_id,request_id),
  ADD CONSTRAINT portfolio_exports_run_unique UNIQUE (tenant_id,run_id),
  ADD CONSTRAINT portfolio_exports_start_command_unique UNIQUE (tenant_id,start_command_id),
  ADD CONSTRAINT portfolio_exports_status_contract CHECK (status IN ('requested','building','ready','failed','expired')),
  ADD CONSTRAINT portfolio_exports_scope_contract CHECK (
    NULLIF(request_id,'') IS NOT NULL AND project_version>0 AND workspace_binding_version>0
    AND NULLIF(workspace_revision,'') IS NOT NULL AND NULLIF(workspace_manifest_hash,'') IS NOT NULL
    AND export_format IN ('html','pdf','zip') AND jsonb_typeof(revision_manifest)='object'
    AND NULLIF(revision_manifest_hash,'') IS NOT NULL
  ),
  ADD CONSTRAINT portfolio_exports_lifecycle_contract CHECK (
    (status='requested' AND started_at IS NULL AND object_ref IS NULL AND object_version IS NULL AND content_hash IS NULL
      AND media_type IS NULL AND byte_size IS NULL AND scan_result_hash IS NULL AND completion_event_id IS NULL
      AND completed_at IS NULL AND failed_at IS NULL AND failure_code IS NULL AND expired_at IS NULL AND expires_at IS NULL)
    OR (status='building' AND started_at IS NOT NULL AND object_ref IS NULL AND object_version IS NULL AND content_hash IS NULL
      AND media_type IS NULL AND byte_size IS NULL AND scan_result_hash IS NULL AND completion_event_id IS NULL
      AND completed_at IS NULL AND failed_at IS NULL AND failure_code IS NULL AND expired_at IS NULL AND expires_at IS NULL)
    OR (status='ready' AND started_at IS NOT NULL AND NULLIF(object_ref,'') IS NOT NULL AND NULLIF(object_version,'') IS NOT NULL
      AND NULLIF(content_hash,'') IS NOT NULL AND NULLIF(media_type,'') IS NOT NULL AND byte_size>0 AND byte_size<=1073741824
      AND NULLIF(scan_result_hash,'') IS NOT NULL AND completion_event_id IS NOT NULL AND completed_at IS NOT NULL
      AND expires_at>completed_at AND failed_at IS NULL AND failure_code IS NULL AND expired_at IS NULL)
    OR (status='failed' AND started_at IS NOT NULL AND object_ref IS NULL AND object_version IS NULL AND content_hash IS NULL
      AND media_type IS NULL AND byte_size IS NULL AND scan_result_hash IS NULL AND completion_event_id IS NOT NULL
      AND completed_at IS NULL AND failed_at IS NOT NULL AND NULLIF(failure_code,'') IS NOT NULL AND expired_at IS NULL AND expires_at IS NULL)
    OR (status='expired' AND started_at IS NOT NULL AND object_ref IS NOT NULL AND object_version IS NOT NULL
      AND content_hash IS NOT NULL AND media_type IS NOT NULL AND byte_size>0 AND scan_result_hash IS NOT NULL
      AND completion_event_id IS NOT NULL AND completed_at IS NOT NULL AND expires_at IS NOT NULL
      AND expired_at>=expires_at AND failed_at IS NULL AND failure_code IS NULL)
  ),
  ADD CONSTRAINT portfolio_exports_tenant_project_fk FOREIGN KEY (tenant_id,project_id)
    REFERENCES product.projects(tenant_id,id) ON DELETE RESTRICT,
  ADD CONSTRAINT portfolio_exports_workspace_binding_fk FOREIGN KEY (tenant_id,workspace_binding_id,workspace_binding_version,workspace_revision,workspace_manifest_hash)
    REFERENCES product.project_workspace_bindings(tenant_id,id,version,head_revision,binding_manifest_hash) ON DELETE RESTRICT,
  ADD CONSTRAINT portfolio_exports_run_fk FOREIGN KEY (tenant_id,run_id)
    REFERENCES agent.runs(tenant_id,id) ON DELETE RESTRICT,
  ADD CONSTRAINT portfolio_exports_start_command_fk FOREIGN KEY (tenant_id,start_command_id)
    REFERENCES agent.outbox(tenant_id,command_id) DEFERRABLE INITIALLY DEFERRED,
  ADD CONSTRAINT portfolio_exports_request_event_fk FOREIGN KEY (request_event_id)
    REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  ADD CONSTRAINT portfolio_exports_completion_event_fk FOREIGN KEY (completion_event_id)
    REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;

CREATE TABLE product.portfolio_export_artifacts (
  tenant_id uuid NOT NULL,
  portfolio_export_id uuid NOT NULL,
  artifact_revision_id uuid NOT NULL,
  artifact_id uuid NOT NULL,
  artifact_revision integer NOT NULL,
  content_hash text NOT NULL,
  ordinal integer NOT NULL,
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (tenant_id,portfolio_export_id,artifact_revision_id),
  CONSTRAINT portfolio_export_artifacts_ordinal_unique UNIQUE (tenant_id,portfolio_export_id,ordinal),
  CONSTRAINT portfolio_export_artifacts_artifact_unique UNIQUE (tenant_id,portfolio_export_id,artifact_id),
  CONSTRAINT portfolio_export_artifacts_scope_contract CHECK (artifact_revision>0 AND ordinal>=0 AND NULLIF(content_hash,'') IS NOT NULL),
  CONSTRAINT portfolio_export_artifacts_export_fk FOREIGN KEY (tenant_id,portfolio_export_id)
    REFERENCES product.portfolio_exports(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT portfolio_export_artifacts_revision_fk FOREIGN KEY (tenant_id,artifact_revision_id,artifact_id,artifact_revision,content_hash)
    REFERENCES product.artifact_revisions(tenant_id,id,artifact_id,revision,content_hash) ON DELETE RESTRICT
);

CREATE TABLE product.portfolio_export_evidence (
  tenant_id uuid NOT NULL,
  portfolio_export_id uuid NOT NULL,
  evidence_id uuid NOT NULL,
  evidence_version bigint NOT NULL,
  evidence_content_hash text NOT NULL,
  ordinal integer NOT NULL,
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (tenant_id,portfolio_export_id,evidence_id),
  CONSTRAINT portfolio_export_evidence_ordinal_unique UNIQUE (tenant_id,portfolio_export_id,ordinal),
  CONSTRAINT portfolio_export_evidence_scope_contract CHECK (evidence_version>0 AND ordinal>=0 AND NULLIF(evidence_content_hash,'') IS NOT NULL),
  CONSTRAINT portfolio_export_evidence_export_fk FOREIGN KEY (tenant_id,portfolio_export_id)
    REFERENCES product.portfolio_exports(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT portfolio_export_evidence_exact_fk FOREIGN KEY (tenant_id,evidence_id,evidence_version,evidence_content_hash)
    REFERENCES product.evidence(tenant_id,id,version,content_hash) ON DELETE RESTRICT
);

ALTER TABLE product.portfolio_export_artifacts ENABLE ROW LEVEL SECURITY;
ALTER TABLE product.portfolio_export_artifacts FORCE ROW LEVEL SECURITY;
CREATE POLICY portfolio_export_artifacts_tenant_isolation ON product.portfolio_export_artifacts
  USING (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid);
CREATE TRIGGER portfolio_export_artifacts_append_only BEFORE UPDATE OR DELETE ON product.portfolio_export_artifacts
  FOR EACH ROW EXECUTE FUNCTION agent.reject_append_only_mutation();

ALTER TABLE product.portfolio_export_evidence ENABLE ROW LEVEL SECURITY;
ALTER TABLE product.portfolio_export_evidence FORCE ROW LEVEL SECURITY;
CREATE POLICY portfolio_export_evidence_tenant_isolation ON product.portfolio_export_evidence
  USING (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid);
CREATE TRIGGER portfolio_export_evidence_append_only BEFORE UPDATE OR DELETE ON product.portfolio_export_evidence
  FOR EACH ROW EXECUTE FUNCTION agent.reject_append_only_mutation();

CREATE FUNCTION product.enforce_portfolio_export_lifecycle() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'portfolio export deletion is forbidden'; END IF;
  IF OLD.status IN ('failed','expired') THEN RAISE EXCEPTION 'terminal portfolio export mutation is forbidden'; END IF;
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id OR NEW.user_id IS DISTINCT FROM OLD.user_id
    OR NEW.project_id IS DISTINCT FROM OLD.project_id OR NEW.request_id IS DISTINCT FROM OLD.request_id
    OR NEW.project_version IS DISTINCT FROM OLD.project_version OR NEW.workspace_binding_id IS DISTINCT FROM OLD.workspace_binding_id
    OR NEW.workspace_binding_version IS DISTINCT FROM OLD.workspace_binding_version OR NEW.workspace_revision IS DISTINCT FROM OLD.workspace_revision
    OR NEW.workspace_manifest_hash IS DISTINCT FROM OLD.workspace_manifest_hash OR NEW.export_format IS DISTINCT FROM OLD.export_format
    OR NEW.revision_manifest IS DISTINCT FROM OLD.revision_manifest OR NEW.revision_manifest_hash IS DISTINCT FROM OLD.revision_manifest_hash
    OR NEW.run_id IS DISTINCT FROM OLD.run_id OR NEW.start_command_id IS DISTINCT FROM OLD.start_command_id
    OR NEW.request_event_id IS DISTINCT FROM OLD.request_event_id OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'immutable portfolio export scope mutation is forbidden';
  END IF;
  IF NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at THEN RAISE EXCEPTION 'portfolio export version must advance exactly once'; END IF;
  IF OLD.status='requested' AND NEW.status IN ('building','failed') THEN RETURN NEW; END IF;
  IF OLD.status='building' AND NEW.status IN ('ready','failed') THEN RETURN NEW; END IF;
  IF OLD.status='ready' AND NEW.status='expired'
    AND NEW.started_at IS NOT DISTINCT FROM OLD.started_at AND NEW.object_ref IS NOT DISTINCT FROM OLD.object_ref
    AND NEW.object_version IS NOT DISTINCT FROM OLD.object_version AND NEW.content_hash IS NOT DISTINCT FROM OLD.content_hash
    AND NEW.media_type IS NOT DISTINCT FROM OLD.media_type AND NEW.byte_size IS NOT DISTINCT FROM OLD.byte_size
    AND NEW.scan_result_hash IS NOT DISTINCT FROM OLD.scan_result_hash
    AND NEW.completion_event_id IS NOT DISTINCT FROM OLD.completion_event_id
    AND NEW.completed_at IS NOT DISTINCT FROM OLD.completed_at AND NEW.expires_at IS NOT DISTINCT FROM OLD.expires_at THEN RETURN NEW; END IF;
  RAISE EXCEPTION 'invalid portfolio export transition % -> %',OLD.status,NEW.status;
END
$$;

CREATE TRIGGER portfolio_exports_lifecycle BEFORE UPDATE OR DELETE ON product.portfolio_exports
  FOR EACH ROW EXECUTE FUNCTION product.enforce_portfolio_export_lifecycle();

CREATE INDEX portfolio_exports_recovery_idx ON product.portfolio_exports(tenant_id,status,updated_at,id)
  WHERE status IN ('requested','building');
CREATE INDEX portfolio_exports_expiry_idx ON product.portfolio_exports(tenant_id,expires_at,id)
  WHERE status='ready';

CREATE FUNCTION agent.list_recoverable_portfolio_tenants(
  p_store_epoch uuid,
  p_after uuid,
  p_limit integer,
  p_shard_index integer,
  p_shard_count integer,
  p_now timestamptz,
  p_stale_before timestamptz
) RETURNS TABLE(tenant_id text)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, product, agent SET row_security = off AS $$
BEGIN
  IF p_store_epoch IS NULL OR p_limit IS NULL OR p_limit<1 OR p_limit>5000
    OR p_shard_count IS NULL OR p_shard_count<1 OR p_shard_index IS NULL OR p_shard_index<0 OR p_shard_index>=p_shard_count
    OR p_now IS NULL OR p_stale_before IS NULL OR p_stale_before>p_now THEN
    RAISE EXCEPTION 'invalid portfolio recovery tenant scan arguments' USING ERRCODE='22023';
  END IF;
  RETURN QUERY
    SELECT p.tenant_id::text
    FROM product.portfolio_exports p
    JOIN agent.events e ON e.tenant_id=p.tenant_id AND e.id=p.request_event_id
      AND e.aggregate_kind='portfolio_export' AND e.aggregate_id=p.id
      AND e.aggregate_version=1 AND e.event_type='PortfolioExportRequested' AND e.event_schema_version=2
    JOIN agent.runs r ON r.tenant_id=p.tenant_id AND r.id=p.run_id
    WHERE e.store_epoch=p_store_epoch AND p.status IN ('requested','building')
      AND (
        (r.status='queued' AND r.pending_command_id=p.start_command_id AND p.status='requested' AND p.updated_at<=p_stale_before)
        OR (r.status='executing' AND r.active_command_id=p.start_command_id AND r.lease_expires_at<=p_now)
        OR (p.status='requested' AND r.status='executing' AND r.active_command_id=p.start_command_id AND p.updated_at<=p_stale_before)
        OR r.status NOT IN ('queued','executing')
        OR (r.status='queued' AND (r.pending_command_id IS DISTINCT FROM p.start_command_id OR p.status<>'requested'))
        OR (r.status='executing' AND r.active_command_id IS DISTINCT FROM p.start_command_id)
      )
      AND (p_after IS NULL OR p.tenant_id>p_after)
      AND ((hashtextextended(p.tenant_id::text,0) & 9223372036854775807) % p_shard_count)=p_shard_index
    GROUP BY p.tenant_id ORDER BY p.tenant_id LIMIT p_limit;
END
$$;

REVOKE ALL ON FUNCTION agent.list_recoverable_portfolio_tenants(uuid,uuid,integer,integer,integer,timestamptz,timestamptz) FROM PUBLIC;

CREATE FUNCTION agent.list_expirable_portfolio_tenants(
  p_store_epoch uuid,
  p_after uuid,
  p_limit integer,
  p_shard_index integer,
  p_shard_count integer,
  p_now timestamptz
) RETURNS TABLE(tenant_id text)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, product, agent SET row_security = off AS $$
BEGIN
  IF p_store_epoch IS NULL OR p_limit IS NULL OR p_limit<1 OR p_limit>5000
    OR p_shard_count IS NULL OR p_shard_count<1 OR p_shard_index IS NULL OR p_shard_index<0 OR p_shard_index>=p_shard_count
    OR p_now IS NULL THEN
    RAISE EXCEPTION 'invalid portfolio expiry tenant scan arguments' USING ERRCODE='22023';
  END IF;
  RETURN QUERY
    SELECT p.tenant_id::text
    FROM product.portfolio_exports p
    JOIN agent.events e ON e.tenant_id=p.tenant_id AND e.id=p.completion_event_id
      AND e.aggregate_kind='portfolio_export' AND e.aggregate_id=p.id
      AND e.aggregate_version=3 AND e.event_type='PortfolioExportCompleted' AND e.event_schema_version=2
    WHERE e.store_epoch=p_store_epoch AND p.status='ready' AND p.expires_at<=p_now
      AND (p_after IS NULL OR p.tenant_id>p_after)
      AND ((hashtextextended(p.tenant_id::text,0) & 9223372036854775807) % p_shard_count)=p_shard_index
    GROUP BY p.tenant_id ORDER BY p.tenant_id LIMIT p_limit;
END
$$;

REVOKE ALL ON FUNCTION agent.list_expirable_portfolio_tenants(uuid,uuid,integer,integer,integer,timestamptz) FROM PUBLIC;
