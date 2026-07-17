DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM public.lites_schema_migrations
    WHERE version=61 AND name='route_planner_run_binding'
  ) THEN
    RAISE EXCEPTION 'route planner run binding migration is not installed';
  END IF;

  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='route_revisions_planner_run_fk')
    OR NOT EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname='product' AND indexname='route_revisions_planner_run_unique')
    OR to_regprocedure('agent.lock_owned_route_planning_mission(uuid,uuid,uuid)') IS NULL THEN
    RAISE EXCEPTION 'route planner run binding contract is incomplete';
  END IF;

  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service')
    AND (NOT has_table_privilege('lites_product_service','agent.conversations','SELECT,INSERT,UPDATE')
      OR NOT has_table_privilege('lites_product_service','agent.run_messages','SELECT,INSERT')
      OR NOT has_function_privilege('lites_product_service','agent.lock_owned_route_planning_mission(uuid,uuid,uuid)','EXECUTE')) THEN
    RAISE EXCEPTION 'product service cannot admit pinned route planner runs';
  END IF;
END
$$;

SELECT json_build_object(
  'status','passed',
  'schema_version',61,
  'route_planner','pinned_run_binding_enforced'
) AS current_schema_verification;
