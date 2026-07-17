DO $$
BEGIN
  IF to_regprocedure('product.lock_project_completion_manifest(uuid,uuid,uuid,bigint,text)') IS NULL THEN
    RAISE EXCEPTION 'project completion fence is missing';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='projects_lifecycle' AND tgenabled='O')
    OR NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='project_milestones_lifecycle' AND tgenabled='O')
    OR NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='project_workspace_bindings_lifecycle' AND tgenabled='O') THEN
    RAISE EXCEPTION 'Create lifecycle trigger is missing';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='product' AND table_name='projects' AND column_name='completion_manifest_hash' AND is_nullable='YES')
    OR NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='product' AND table_name='projects' AND column_name='last_event_id' AND is_nullable='NO')
    OR NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='product' AND table_name='project_test_runs' AND column_name='result_manifest_hash' AND is_nullable='NO') THEN
    RAISE EXCEPTION 'Create integrity columns are missing';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service')
    AND (NOT has_table_privilege('lites_product_service','product.projects','SELECT,INSERT,UPDATE')
      OR NOT has_function_privilege('lites_product_service','product.lock_project_completion_manifest(uuid,uuid,uuid,bigint,text)','EXECUTE')) THEN
    RAISE EXCEPTION 'product service Create grants are incomplete';
  END IF;
END
$$;
