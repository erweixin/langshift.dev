DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM product.projects)
    OR EXISTS (SELECT 1 FROM product.project_milestones)
    OR EXISTS (SELECT 1 FROM product.project_workspace_bindings)
    OR EXISTS (SELECT 1 FROM product.project_test_runs) THEN
    RAISE EXCEPTION 'Create project protocol backfill required before migration 70' USING ERRCODE='55000';
  END IF;
END
$$;

ALTER TABLE product.projects
  ADD COLUMN brief_hash text,
  ADD COLUMN brief_manifest_hash text,
  ADD COLUMN reflection_hash text,
  ADD COLUMN reflection_manifest_hash text,
  ADD COLUMN completion_manifest jsonb,
  ADD COLUMN completion_manifest_hash text,
  ADD COLUMN created_event_id uuid,
  ADD COLUMN last_event_id uuid,
  ADD COLUMN completion_event_id uuid,
  ALTER COLUMN brief_hash SET NOT NULL,
  ALTER COLUMN brief_manifest_hash SET NOT NULL,
  ALTER COLUMN created_event_id SET NOT NULL,
  ALTER COLUMN last_event_id SET NOT NULL,
  ADD CONSTRAINT projects_tenant_id_id_version_unique UNIQUE (tenant_id,id,version),
  ADD CONSTRAINT projects_scope_contract CHECK (
    length(title) BETWEEN 1 AND 200 AND NULLIF(brief_ref,'') IS NOT NULL
    AND brief_hash ~ '^[0-9a-f]{64}$' AND brief_manifest_hash ~ '^[0-9a-f]{64}$'
  ),
  ADD CONSTRAINT projects_completion_contract CHECK (
    (status IN ('draft','active','blocked') AND reflection_ref IS NULL AND reflection_hash IS NULL
      AND reflection_manifest_hash IS NULL AND completion_manifest IS NULL
      AND completion_manifest_hash IS NULL AND completion_event_id IS NULL AND completed_at IS NULL)
    OR (status='completed' AND NULLIF(reflection_ref,'') IS NOT NULL
      AND reflection_hash ~ '^[0-9a-f]{64}$' AND reflection_manifest_hash ~ '^[0-9a-f]{64}$'
      AND jsonb_typeof(completion_manifest)='object' AND completion_manifest_hash ~ '^[0-9a-f]{64}$'
      AND completion_event_id IS NOT NULL AND completed_at IS NOT NULL)
    OR (status='archived' AND (
      (reflection_ref IS NULL AND reflection_hash IS NULL AND reflection_manifest_hash IS NULL
        AND completion_manifest IS NULL AND completion_manifest_hash IS NULL AND completion_event_id IS NULL AND completed_at IS NULL)
      OR (NULLIF(reflection_ref,'') IS NOT NULL AND reflection_hash ~ '^[0-9a-f]{64}$'
        AND reflection_manifest_hash ~ '^[0-9a-f]{64}$' AND jsonb_typeof(completion_manifest)='object'
        AND completion_manifest_hash ~ '^[0-9a-f]{64}$' AND completion_event_id IS NOT NULL AND completed_at IS NOT NULL)
    ))
  ),
  ADD CONSTRAINT projects_mission_scope_fk FOREIGN KEY (tenant_id,mission_id)
    REFERENCES product.missions(tenant_id,id) ON DELETE RESTRICT,
  ADD CONSTRAINT projects_route_scope_fk FOREIGN KEY (tenant_id,accepted_route_revision_id)
    REFERENCES product.route_revisions(tenant_id,id) ON DELETE RESTRICT,
  ADD CONSTRAINT projects_created_event_fk FOREIGN KEY (created_event_id)
    REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  ADD CONSTRAINT projects_last_event_fk FOREIGN KEY (last_event_id)
    REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  ADD CONSTRAINT projects_completion_event_fk FOREIGN KEY (completion_event_id)
    REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE product.project_milestones
  ADD COLUMN acceptance_spec_hash text,
  ADD COLUMN result_ref text,
  ADD COLUMN result_hash text,
  ADD COLUMN verification_test_run_id uuid,
  ADD COLUMN created_event_id uuid,
  ADD COLUMN last_event_id uuid,
  ALTER COLUMN acceptance_spec_hash SET NOT NULL,
  ALTER COLUMN created_event_id SET NOT NULL,
  ALTER COLUMN last_event_id SET NOT NULL,
  ADD CONSTRAINT project_milestones_tenant_id_id_unique UNIQUE (tenant_id,id),
  ADD CONSTRAINT project_milestones_scope_contract CHECK (
    sequence>0 AND length(title) BETWEEN 1 AND 200 AND jsonb_typeof(acceptance_spec)='object'
    AND acceptance_spec_hash ~ '^[0-9a-f]{64}$'
  ),
  ADD CONSTRAINT project_milestones_result_contract CHECK (
    (status IN ('planned','in_progress') AND result_ref IS NULL AND result_hash IS NULL AND verification_test_run_id IS NULL AND completed_at IS NULL)
    OR (status IN ('submitted','rework') AND NULLIF(result_ref,'') IS NOT NULL AND result_hash ~ '^[0-9a-f]{64}$' AND verification_test_run_id IS NULL AND completed_at IS NULL)
    OR (status='verified' AND NULLIF(result_ref,'') IS NOT NULL AND result_hash ~ '^[0-9a-f]{64}$' AND verification_test_run_id IS NOT NULL AND completed_at IS NULL)
    OR (status='completed' AND NULLIF(result_ref,'') IS NOT NULL AND result_hash ~ '^[0-9a-f]{64}$' AND verification_test_run_id IS NOT NULL AND completed_at IS NOT NULL)
  ),
  ADD CONSTRAINT project_milestones_created_event_fk FOREIGN KEY (created_event_id)
    REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  ADD CONSTRAINT project_milestones_last_event_fk FOREIGN KEY (last_event_id)
    REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE product.project_workspace_bindings
  ADD COLUMN binding_manifest jsonb,
  ADD COLUMN created_event_id uuid,
  ADD COLUMN last_event_id uuid,
  ALTER COLUMN binding_manifest SET NOT NULL,
  ALTER COLUMN created_event_id SET NOT NULL,
  ALTER COLUMN last_event_id SET NOT NULL,
  ADD CONSTRAINT project_workspace_bindings_manifest_contract CHECK (
    length(branch_name) BETWEEN 1 AND 240 AND branch_name ~ '^[A-Za-z0-9._/-]+$'
    AND NULLIF(base_revision,'') IS NOT NULL AND NULLIF(head_revision,'') IS NOT NULL
    AND binding_manifest_hash ~ '^[0-9a-f]{64}$' AND jsonb_typeof(binding_manifest)='object'
  ),
  ADD CONSTRAINT project_workspace_bindings_created_event_fk FOREIGN KEY (created_event_id)
    REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  ADD CONSTRAINT project_workspace_bindings_last_event_fk FOREIGN KEY (last_event_id)
    REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE product.project_test_runs
  ADD COLUMN result_manifest_hash text,
  ADD COLUMN evidence_id uuid,
  ADD COLUMN recorded_event_id uuid,
  ALTER COLUMN result_manifest_hash SET NOT NULL,
  ALTER COLUMN evidence_id SET NOT NULL,
  ALTER COLUMN recorded_event_id SET NOT NULL,
  ADD CONSTRAINT project_test_runs_tenant_id_id_unique UNIQUE (tenant_id,id),
  ADD CONSTRAINT project_test_runs_contract CHECK (
    validation_kind IN ('deterministic_test','rubric_review') AND result IN ('passed','failed')
    AND NULLIF(workspace_revision,'') IS NOT NULL AND jsonb_typeof(result_manifest)='object'
    AND result_manifest_hash ~ '^[0-9a-f]{64}$'
  ),
  ADD CONSTRAINT project_test_runs_evidence_fk FOREIGN KEY (tenant_id,evidence_id)
    REFERENCES product.evidence(tenant_id,id) ON DELETE RESTRICT,
  ADD CONSTRAINT project_test_runs_event_fk FOREIGN KEY (recorded_event_id)
    REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE product.project_milestones
  ADD CONSTRAINT project_milestones_verification_run_fk FOREIGN KEY (tenant_id,verification_test_run_id)
    REFERENCES product.project_test_runs(tenant_id,id) ON DELETE RESTRICT;

CREATE FUNCTION product.enforce_project_lifecycle() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'project deletion is forbidden'; END IF;
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.user_id IS DISTINCT FROM OLD.user_id OR NEW.mission_id IS DISTINCT FROM OLD.mission_id
    OR NEW.accepted_route_revision_id IS DISTINCT FROM OLD.accepted_route_revision_id
    OR NEW.project_kind IS DISTINCT FROM OLD.project_kind OR NEW.title IS DISTINCT FROM OLD.title
    OR NEW.brief_ref IS DISTINCT FROM OLD.brief_ref OR NEW.brief_hash IS DISTINCT FROM OLD.brief_hash
    OR NEW.brief_manifest_hash IS DISTINCT FROM OLD.brief_manifest_hash OR NEW.created_event_id IS DISTINCT FROM OLD.created_event_id
    OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'immutable project scope mutation is forbidden';
  END IF;
  IF NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at OR NEW.last_event_id IS NOT DISTINCT FROM OLD.last_event_id THEN
    RAISE EXCEPTION 'project version/event must advance exactly once';
  END IF;
  IF OLD.status='archived' OR (OLD.status='completed' AND NEW.status<>'archived') THEN
    RAISE EXCEPTION 'terminal project mutation is forbidden';
  END IF;
  IF OLD.status='completed' AND (
    NEW.reflection_ref IS DISTINCT FROM OLD.reflection_ref
    OR NEW.reflection_hash IS DISTINCT FROM OLD.reflection_hash
    OR NEW.reflection_manifest_hash IS DISTINCT FROM OLD.reflection_manifest_hash
    OR NEW.completion_manifest IS DISTINCT FROM OLD.completion_manifest
    OR NEW.completion_manifest_hash IS DISTINCT FROM OLD.completion_manifest_hash
    OR NEW.completion_event_id IS DISTINCT FROM OLD.completion_event_id
    OR NEW.completed_at IS DISTINCT FROM OLD.completed_at
  ) THEN
    RAISE EXCEPTION 'completed project evidence is immutable';
  END IF;
  IF (OLD.status='draft' AND NEW.status IN ('active','archived'))
    OR (OLD.status='active' AND NEW.status IN ('blocked','completed','archived'))
    OR (OLD.status='blocked' AND NEW.status IN ('active','completed','archived'))
    OR (OLD.status='completed' AND NEW.status='archived')
    OR (OLD.status=NEW.status AND OLD.status IN ('draft','active','blocked')) THEN RETURN NEW; END IF;
  RAISE EXCEPTION 'invalid project transition % -> %',OLD.status,NEW.status;
END
$$;
CREATE TRIGGER projects_lifecycle BEFORE UPDATE OR DELETE ON product.projects
  FOR EACH ROW EXECUTE FUNCTION product.enforce_project_lifecycle();

CREATE FUNCTION product.enforce_project_milestone_lifecycle() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'milestone deletion is forbidden'; END IF;
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.user_id IS DISTINCT FROM OLD.user_id OR NEW.project_id IS DISTINCT FROM OLD.project_id
    OR NEW.sequence IS DISTINCT FROM OLD.sequence OR NEW.required IS DISTINCT FROM OLD.required
    OR NEW.title IS DISTINCT FROM OLD.title OR NEW.acceptance_spec IS DISTINCT FROM OLD.acceptance_spec
    OR NEW.acceptance_spec_hash IS DISTINCT FROM OLD.acceptance_spec_hash
    OR NEW.created_event_id IS DISTINCT FROM OLD.created_event_id OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'immutable milestone scope mutation is forbidden';
  END IF;
  IF NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at OR NEW.last_event_id IS NOT DISTINCT FROM OLD.last_event_id THEN
    RAISE EXCEPTION 'milestone version/event must advance exactly once';
  END IF;
  IF OLD.result_ref IS NOT NULL AND OLD.status<>'rework' AND (NEW.result_ref IS DISTINCT FROM OLD.result_ref OR NEW.result_hash IS DISTINCT FROM OLD.result_hash) THEN
    RAISE EXCEPTION 'milestone result binding is immutable';
  END IF;
  IF (OLD.status='planned' AND NEW.status='in_progress')
    OR (OLD.status='in_progress' AND NEW.status='submitted')
    OR (OLD.status='submitted' AND NEW.status IN ('verified','rework'))
    OR (OLD.status='rework' AND NEW.status='submitted')
    OR (OLD.status='verified' AND NEW.status='completed') THEN RETURN NEW; END IF;
  RAISE EXCEPTION 'invalid milestone transition % -> %',OLD.status,NEW.status;
END
$$;
CREATE TRIGGER project_milestones_lifecycle BEFORE UPDATE OR DELETE ON product.project_milestones
  FOR EACH ROW EXECUTE FUNCTION product.enforce_project_milestone_lifecycle();

CREATE FUNCTION product.enforce_project_workspace_binding_lifecycle() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'workspace binding deletion is forbidden'; END IF;
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.user_id IS DISTINCT FROM OLD.user_id OR NEW.project_id IS DISTINCT FROM OLD.project_id
    OR NEW.workspace_id IS DISTINCT FROM OLD.workspace_id OR NEW.branch_name IS DISTINCT FROM OLD.branch_name
    OR NEW.base_revision IS DISTINCT FROM OLD.base_revision OR NEW.created_event_id IS DISTINCT FROM OLD.created_event_id
    OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'immutable workspace binding scope mutation is forbidden';
  END IF;
  IF NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at OR NEW.head_revision=OLD.head_revision
    OR NEW.binding_manifest_hash=OLD.binding_manifest_hash OR NEW.last_event_id IS NOT DISTINCT FROM OLD.last_event_id THEN
    RAISE EXCEPTION 'workspace binding head must CAS advance exactly once';
  END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER project_workspace_bindings_lifecycle BEFORE UPDATE OR DELETE ON product.project_workspace_bindings
  FOR EACH ROW EXECUTE FUNCTION product.enforce_project_workspace_binding_lifecycle();

CREATE FUNCTION product.lock_project_completion_manifest(
  p_tenant uuid,p_user uuid,p_project uuid,p_expected_version bigint,p_workspace_revision text
) RETURNS jsonb
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,product,agent SET row_security=off AS $$
DECLARE
  project_row product.projects%ROWTYPE;
  workspace_row product.project_workspace_bindings%ROWTYPE;
  required_count integer;
  completed_count integer;
  passing_count integer;
  artifact_count integer;
  manifest jsonb;
BEGIN
  IF p_tenant IS NULL OR p_user IS NULL OR p_project IS NULL OR p_expected_version<1 OR NULLIF(p_workspace_revision,'') IS NULL THEN
    RAISE EXCEPTION 'invalid project completion fence' USING ERRCODE='22023';
  END IF;
  SELECT * INTO project_row FROM product.projects
    WHERE tenant_id=p_tenant AND user_id=p_user AND id=p_project FOR UPDATE;
  IF NOT FOUND OR project_row.version<>p_expected_version OR project_row.status NOT IN ('active','blocked') THEN RETURN NULL; END IF;
  SELECT * INTO workspace_row FROM product.project_workspace_bindings
    WHERE tenant_id=p_tenant AND user_id=p_user AND project_id=p_project FOR UPDATE;
  IF NOT FOUND OR workspace_row.head_revision<>p_workspace_revision THEN RETURN NULL; END IF;
  PERFORM 1 FROM product.project_milestones WHERE tenant_id=p_tenant AND user_id=p_user AND project_id=p_project FOR UPDATE;
  PERFORM 1 FROM product.artifacts WHERE tenant_id=p_tenant AND user_id=p_user AND project_id=p_project FOR UPDATE;
  SELECT count(*) FILTER (WHERE required),count(*) FILTER (WHERE required AND status='completed')
    INTO required_count,completed_count FROM product.project_milestones
    WHERE tenant_id=p_tenant AND user_id=p_user AND project_id=p_project;
  SELECT count(*) INTO passing_count FROM product.project_test_runs
    WHERE tenant_id=p_tenant AND user_id=p_user AND project_id=p_project AND workspace_revision=p_workspace_revision AND result='passed';
  SELECT count(*) INTO artifact_count FROM product.artifact_revisions r
    JOIN product.artifacts a ON a.tenant_id=r.tenant_id AND a.id=r.artifact_id
    WHERE r.tenant_id=p_tenant AND r.user_id=p_user AND r.project_id=p_project
      AND r.workspace_revision=p_workspace_revision AND r.scan_status='passed' AND a.status='ready'
      AND EXISTS (SELECT 1 FROM product.artifact_revision_evidence e WHERE e.tenant_id=r.tenant_id AND e.artifact_revision_id=r.id);
  IF required_count<2 OR completed_count<>required_count OR passing_count<1 OR artifact_count<1 THEN RETURN NULL; END IF;
  SELECT jsonb_build_object(
    'schema_version',1,
    'project',jsonb_build_object('id',project_row.id,'version',project_row.version,'mission_id',project_row.mission_id,'route_revision_id',project_row.accepted_route_revision_id),
    'workspace',jsonb_build_object('binding_id',workspace_row.id,'version',workspace_row.version,'revision',workspace_row.head_revision,'manifest_hash',workspace_row.binding_manifest_hash),
    'milestones',(SELECT jsonb_agg(jsonb_build_object('id',id,'version',version,'sequence',sequence,'required',required,'status',status,'result_hash',result_hash,'verification_test_run_id',verification_test_run_id) ORDER BY sequence,id) FROM product.project_milestones WHERE tenant_id=p_tenant AND user_id=p_user AND project_id=p_project),
    'passing_test_runs',(SELECT jsonb_agg(jsonb_build_object('id',id,'version',version,'milestone_id',milestone_id,'validation_kind',validation_kind,'workspace_revision',workspace_revision,'result_manifest_hash',result_manifest_hash,'evidence_id',evidence_id) ORDER BY recorded_at,id) FROM product.project_test_runs WHERE tenant_id=p_tenant AND user_id=p_user AND project_id=p_project AND workspace_revision=p_workspace_revision AND result='passed'),
    'artifact_revisions',(SELECT jsonb_agg(jsonb_build_object('id',r.id,'artifact_id',r.artifact_id,'revision',r.revision,'content_hash',r.content_hash,'object_version',r.object_version,'workspace_revision',r.workspace_revision,'evidence_manifest_hash',r.evidence_manifest_hash) ORDER BY r.artifact_id,r.revision) FROM product.artifact_revisions r JOIN product.artifacts a ON a.tenant_id=r.tenant_id AND a.id=r.artifact_id WHERE r.tenant_id=p_tenant AND r.user_id=p_user AND r.project_id=p_project AND r.workspace_revision=p_workspace_revision AND r.scan_status='passed' AND a.status='ready' AND EXISTS (SELECT 1 FROM product.artifact_revision_evidence e WHERE e.tenant_id=r.tenant_id AND e.artifact_revision_id=r.id))
  ) INTO manifest;
  RETURN manifest;
END
$$;
REVOKE ALL ON FUNCTION product.lock_project_completion_manifest(uuid,uuid,uuid,bigint,text) FROM PUBLIC;

CREATE INDEX projects_user_updated_idx ON product.projects(tenant_id,user_id,updated_at DESC,id DESC);
CREATE INDEX project_milestones_completion_idx ON product.project_milestones(tenant_id,project_id,required,status,sequence);
CREATE INDEX project_test_runs_completion_idx ON product.project_test_runs(tenant_id,project_id,workspace_revision,result,recorded_at);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service') THEN
    GRANT SELECT,INSERT,UPDATE ON product.projects,product.project_milestones,product.project_workspace_bindings,product.artifacts TO lites_product_service;
    GRANT SELECT,INSERT ON product.project_test_runs,product.artifact_revisions,product.artifact_revision_evidence,product.portfolio_exports,product.portfolio_export_artifacts,product.portfolio_export_evidence TO lites_product_service;
    GRANT EXECUTE ON FUNCTION product.lock_project_completion_manifest(uuid,uuid,uuid,bigint,text) TO lites_product_service;
  END IF;
END
$$;
