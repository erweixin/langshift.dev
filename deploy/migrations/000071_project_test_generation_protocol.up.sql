CREATE TABLE product.project_test_generations (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version>0),
  request_id text NOT NULL,
  project_id uuid NOT NULL,
  project_version bigint NOT NULL CHECK (project_version>0),
  milestone_id uuid NOT NULL,
  milestone_version bigint NOT NULL CHECK (milestone_version>0),
  workspace_binding_id uuid NOT NULL,
  workspace_binding_version bigint NOT NULL CHECK (workspace_binding_version>0),
  workspace_revision text NOT NULL,
  workspace_manifest_hash text NOT NULL,
  validation_kind text NOT NULL CHECK (validation_kind IN ('deterministic_test','rubric_review')),
  validation_spec jsonb NOT NULL CHECK (jsonb_typeof(validation_spec)='object'),
  validation_spec_hash text NOT NULL CHECK (validation_spec_hash ~ '^[0-9a-f]{64}$'),
  status text NOT NULL CHECK (status IN ('generating','succeeded','failed','superseded')),
  input_manifest jsonb NOT NULL CHECK (jsonb_typeof(input_manifest)='object'),
  input_manifest_hash text NOT NULL CHECK (input_manifest_hash ~ '^[0-9a-f]{64}$'),
  behavior_profile text NOT NULL CHECK (behavior_profile='evaluator'),
  behavior_environment text NOT NULL CHECK (behavior_environment IN ('staging','production')),
  behavior_channel_id uuid NOT NULL,
  behavior_channel_sequence bigint NOT NULL CHECK (behavior_channel_sequence>0),
  behavior_snapshot_id text NOT NULL,
  behavior_activated_at timestamptz NOT NULL,
  evaluator_run_id uuid NOT NULL,
  project_test_run_id uuid,
  evidence_id uuid,
  failure_reason text,
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  completed_at timestamptz,
  CONSTRAINT project_test_generations_tenant_id_id_unique UNIQUE (tenant_id,id),
  CONSTRAINT project_test_generations_request_unique UNIQUE (tenant_id,user_id,request_id),
  CONSTRAINT project_test_generations_run_unique UNIQUE (tenant_id,evaluator_run_id),
  CONSTRAINT project_test_generations_result_unique UNIQUE (tenant_id,project_test_run_id),
  CONSTRAINT project_test_generations_evidence_unique UNIQUE (tenant_id,evidence_id),
  CONSTRAINT project_test_generations_project_fk FOREIGN KEY (tenant_id,project_id)
    REFERENCES product.projects(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT project_test_generations_milestone_fk FOREIGN KEY (tenant_id,milestone_id)
    REFERENCES product.project_milestones(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT project_test_generations_workspace_fk FOREIGN KEY (tenant_id,workspace_binding_id)
    REFERENCES product.project_workspace_bindings(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT project_test_generations_run_fk FOREIGN KEY (tenant_id,evaluator_run_id)
    REFERENCES agent.runs(tenant_id,id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  CONSTRAINT project_test_generations_result_fk FOREIGN KEY (tenant_id,project_test_run_id)
    REFERENCES product.project_test_runs(tenant_id,id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  CONSTRAINT project_test_generations_evidence_fk FOREIGN KEY (tenant_id,evidence_id)
    REFERENCES product.evidence(tenant_id,id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  CONSTRAINT project_test_generations_scope_contract CHECK (
    NULLIF(request_id,'') IS NOT NULL AND NULLIF(workspace_revision,'') IS NOT NULL
    AND workspace_manifest_hash ~ '^[0-9a-f]{64}$' AND NULLIF(behavior_snapshot_id,'') IS NOT NULL
  ),
  CONSTRAINT project_test_generations_lifecycle_contract CHECK (
    (status='generating' AND project_test_run_id IS NULL AND evidence_id IS NULL AND failure_reason IS NULL AND completed_at IS NULL)
    OR (status='succeeded' AND project_test_run_id IS NOT NULL AND evidence_id IS NOT NULL AND failure_reason IS NULL AND completed_at IS NOT NULL)
    OR (status IN ('failed','superseded') AND project_test_run_id IS NULL AND evidence_id IS NULL
      AND char_length(failure_reason) BETWEEN 1 AND 128 AND completed_at IS NOT NULL)
  )
);

ALTER TABLE product.project_test_generations ENABLE ROW LEVEL SECURITY;
ALTER TABLE product.project_test_generations FORCE ROW LEVEL SECURITY;
CREATE POLICY project_test_generations_tenant_isolation ON product.project_test_generations
  USING (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid);

CREATE FUNCTION product.enforce_project_test_generation_lifecycle() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'project test generation deletion is forbidden'; END IF;
  IF OLD.status<>'generating' THEN RAISE EXCEPTION 'terminal project test generation mutation is forbidden'; END IF;
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.user_id IS DISTINCT FROM OLD.user_id OR NEW.request_id IS DISTINCT FROM OLD.request_id
    OR NEW.project_id IS DISTINCT FROM OLD.project_id OR NEW.project_version IS DISTINCT FROM OLD.project_version
    OR NEW.milestone_id IS DISTINCT FROM OLD.milestone_id OR NEW.milestone_version IS DISTINCT FROM OLD.milestone_version
    OR NEW.workspace_binding_id IS DISTINCT FROM OLD.workspace_binding_id
    OR NEW.workspace_binding_version IS DISTINCT FROM OLD.workspace_binding_version
    OR NEW.workspace_revision IS DISTINCT FROM OLD.workspace_revision
    OR NEW.workspace_manifest_hash IS DISTINCT FROM OLD.workspace_manifest_hash
    OR NEW.validation_kind IS DISTINCT FROM OLD.validation_kind OR NEW.validation_spec IS DISTINCT FROM OLD.validation_spec
    OR NEW.validation_spec_hash IS DISTINCT FROM OLD.validation_spec_hash
    OR NEW.input_manifest IS DISTINCT FROM OLD.input_manifest OR NEW.input_manifest_hash IS DISTINCT FROM OLD.input_manifest_hash
    OR NEW.behavior_profile IS DISTINCT FROM OLD.behavior_profile OR NEW.behavior_environment IS DISTINCT FROM OLD.behavior_environment
    OR NEW.behavior_channel_id IS DISTINCT FROM OLD.behavior_channel_id
    OR NEW.behavior_channel_sequence IS DISTINCT FROM OLD.behavior_channel_sequence
    OR NEW.behavior_snapshot_id IS DISTINCT FROM OLD.behavior_snapshot_id
    OR NEW.behavior_activated_at IS DISTINCT FROM OLD.behavior_activated_at
    OR NEW.evaluator_run_id IS DISTINCT FROM OLD.evaluator_run_id OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'immutable project test generation scope mutation is forbidden';
  END IF;
  IF NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at OR NEW.status NOT IN ('succeeded','failed','superseded') THEN
    RAISE EXCEPTION 'invalid project test generation transition';
  END IF;
  RETURN NEW;
END
$$;

CREATE TRIGGER project_test_generations_lifecycle BEFORE UPDATE OR DELETE ON product.project_test_generations
  FOR EACH ROW EXECUTE FUNCTION product.enforce_project_test_generation_lifecycle();

CREATE FUNCTION agent.lock_owned_project_evaluation(
  p_tenant_id uuid,
  p_user_id uuid,
  p_project_id uuid,
  p_project_version bigint,
  p_milestone_id uuid,
  p_milestone_version bigint,
  p_workspace_revision text
) RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, agent, product
SET row_security = off
AS $$
DECLARE admissible boolean;
BEGIN
  SELECT true INTO admissible
  FROM product.projects p
  JOIN product.missions m ON m.tenant_id=p.tenant_id AND m.id=p.mission_id
  JOIN product.project_milestones pm ON pm.tenant_id=p.tenant_id AND pm.project_id=p.id AND pm.id=p_milestone_id
  JOIN product.project_workspace_bindings w ON w.tenant_id=p.tenant_id AND w.project_id=p.id
  WHERE p.tenant_id=p_tenant_id AND p.user_id=p_user_id AND p.id=p_project_id
    AND p.version=p_project_version AND p.status IN ('active','blocked')
    AND m.user_id=p_user_id AND m.status='active'
    AND pm.user_id=p_user_id AND pm.version=p_milestone_version AND pm.status IN ('submitted','rework')
    AND w.user_id=p_user_id AND w.head_revision=p_workspace_revision
  FOR UPDATE OF p,pm,w;
  RETURN COALESCE(admissible,false);
END
$$;

CREATE FUNCTION agent.list_project_test_reconciliation_tenants(
  p_after_tenant uuid,
  p_limit integer
) RETURNS TABLE(tenant_id uuid)
LANGUAGE sql
SECURITY DEFINER
SET search_path = pg_catalog, agent, product
SET row_security = off
AS $$
  SELECT DISTINCT g.tenant_id
  FROM product.project_test_generations g
  JOIN agent.runs r ON r.tenant_id=g.tenant_id AND r.id=g.evaluator_run_id
  WHERE g.status='generating' AND r.status IN ('succeeded','failed','cancelled','expired')
    AND (p_after_tenant IS NULL OR g.tenant_id>p_after_tenant)
  ORDER BY g.tenant_id
  LIMIT LEAST(GREATEST(p_limit,1),5000)
$$;

REVOKE ALL ON FUNCTION agent.lock_owned_project_evaluation(uuid,uuid,uuid,bigint,uuid,bigint,text) FROM PUBLIC;
REVOKE ALL ON FUNCTION agent.list_project_test_reconciliation_tenants(uuid,integer) FROM PUBLIC;

CREATE INDEX project_test_generations_reconcile_idx
  ON product.project_test_generations(tenant_id,status,updated_at,id);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service') THEN
    GRANT SELECT,INSERT,UPDATE ON product.project_test_generations TO lites_product_service;
    GRANT EXECUTE ON FUNCTION agent.lock_owned_project_evaluation(uuid,uuid,uuid,bigint,uuid,bigint,text) TO lites_product_service;
    GRANT EXECUTE ON FUNCTION agent.list_project_test_reconciliation_tenants(uuid,integer) TO lites_product_service;
  END IF;
END
$$;
