DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM public.lites_schema_migrations
    WHERE version=63 AND name='daily_task_generation_protocol'
  ) THEN
    RAISE EXCEPTION 'daily task generation protocol migration is not installed';
  END IF;

  IF to_regclass('product.daily_task_generations') IS NULL
    OR NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='daily_task_generations_lifecycle_contract')
    OR NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='daily_tasks_lifecycle_contract')
    OR NOT EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname='product' AND indexname='daily_tasks_one_daily_commitment')
    OR to_regprocedure('agent.lock_owned_daily_planning_mission(uuid,uuid,uuid,uuid,bigint)') IS NULL THEN
    RAISE EXCEPTION 'daily task generation protocol is incomplete';
  END IF;

  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service')
    AND (NOT has_table_privilege('lites_product_service','product.daily_task_generations','SELECT,INSERT,UPDATE')
      OR NOT has_table_privilege('lites_product_service','product.daily_tasks','SELECT,INSERT,UPDATE')
      OR NOT has_function_privilege('lites_product_service','agent.lock_owned_daily_planning_mission(uuid,uuid,uuid,uuid,bigint)','EXECUTE')) THEN
    RAISE EXCEPTION 'product service daily task privileges are incomplete';
  END IF;
END
$$;

SELECT json_build_object(
  'status','passed',
  'schema_version',63,
  'daily_task_generation','focus_and_accepted_route_bound'
) AS current_schema_verification;
