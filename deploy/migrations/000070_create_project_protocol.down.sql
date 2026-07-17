DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service') THEN
    REVOKE EXECUTE ON FUNCTION product.lock_project_completion_manifest(uuid,uuid,uuid,bigint,text) FROM lites_product_service;
    REVOKE INSERT ON product.projects FROM lites_product_service;
    REVOKE SELECT,INSERT,UPDATE ON product.project_milestones FROM lites_product_service;
    REVOKE INSERT,UPDATE ON product.project_workspace_bindings FROM lites_product_service;
    REVOKE SELECT,INSERT ON product.project_test_runs FROM lites_product_service;
  END IF;
END
$$;

DROP FUNCTION product.lock_project_completion_manifest(uuid,uuid,uuid,bigint,text);
DROP INDEX product.project_test_runs_completion_idx;
DROP INDEX product.project_milestones_completion_idx;
DROP INDEX product.projects_user_updated_idx;
DROP TRIGGER project_workspace_bindings_lifecycle ON product.project_workspace_bindings;
DROP FUNCTION product.enforce_project_workspace_binding_lifecycle();
DROP TRIGGER project_milestones_lifecycle ON product.project_milestones;
DROP FUNCTION product.enforce_project_milestone_lifecycle();
DROP TRIGGER projects_lifecycle ON product.projects;
DROP FUNCTION product.enforce_project_lifecycle();

ALTER TABLE product.project_milestones DROP CONSTRAINT project_milestones_verification_run_fk;
ALTER TABLE product.project_test_runs
  DROP CONSTRAINT project_test_runs_event_fk,
  DROP CONSTRAINT project_test_runs_evidence_fk,
  DROP CONSTRAINT project_test_runs_contract,
  DROP CONSTRAINT project_test_runs_tenant_id_id_unique,
  DROP COLUMN recorded_event_id,
  DROP COLUMN evidence_id,
  DROP COLUMN result_manifest_hash;
ALTER TABLE product.project_workspace_bindings
  DROP CONSTRAINT project_workspace_bindings_last_event_fk,
  DROP CONSTRAINT project_workspace_bindings_created_event_fk,
  DROP CONSTRAINT project_workspace_bindings_manifest_contract,
  DROP COLUMN last_event_id,
  DROP COLUMN created_event_id,
  DROP COLUMN binding_manifest;
ALTER TABLE product.project_milestones
  DROP CONSTRAINT project_milestones_last_event_fk,
  DROP CONSTRAINT project_milestones_created_event_fk,
  DROP CONSTRAINT project_milestones_result_contract,
  DROP CONSTRAINT project_milestones_scope_contract,
  DROP CONSTRAINT project_milestones_tenant_id_id_unique,
  DROP COLUMN last_event_id,
  DROP COLUMN created_event_id,
  DROP COLUMN verification_test_run_id,
  DROP COLUMN result_hash,
  DROP COLUMN result_ref,
  DROP COLUMN acceptance_spec_hash;
ALTER TABLE product.projects
  DROP CONSTRAINT projects_completion_event_fk,
  DROP CONSTRAINT projects_last_event_fk,
  DROP CONSTRAINT projects_created_event_fk,
  DROP CONSTRAINT projects_route_scope_fk,
  DROP CONSTRAINT projects_mission_scope_fk,
  DROP CONSTRAINT projects_completion_contract,
  DROP CONSTRAINT projects_scope_contract,
  DROP CONSTRAINT projects_tenant_id_id_version_unique,
  DROP COLUMN completion_event_id,
  DROP COLUMN last_event_id,
  DROP COLUMN created_event_id,
  DROP COLUMN completion_manifest_hash,
  DROP COLUMN completion_manifest,
  DROP COLUMN reflection_manifest_hash,
  DROP COLUMN reflection_hash,
  DROP COLUMN brief_manifest_hash,
  DROP COLUMN brief_hash;
