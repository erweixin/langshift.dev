DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='project_test_generations_lifecycle_contract')
    OR NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='project_test_generations_lifecycle' AND tgenabled='O')
    OR NOT EXISTS (SELECT 1 FROM pg_proc WHERE proname='lock_owned_project_evaluation')
    OR NOT EXISTS (SELECT 1 FROM pg_proc WHERE proname='list_project_test_reconciliation_tenants') THEN
    RAISE EXCEPTION 'project test generation protocol incomplete';
  END IF;
END
$$;

SELECT json_build_object('status','passed','schema_version',71,'project_test_generation','agent_evaluated_and_fenced') AS current_schema_verification;
