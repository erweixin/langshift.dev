DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM public.lites_schema_migrations
    WHERE version=60 AND name='route_revision_planner_integrity'
  ) THEN
    RAISE EXCEPTION 'route revision planner integrity migration is not installed';
  END IF;

  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='route_revisions_payload_integrity')
    OR NOT EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname='product' AND indexname='route_revisions_planner_command_unique')
    OR NOT EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname='product' AND indexname='route_revisions_owner_created_idx') THEN
    RAISE EXCEPTION 'route payload, command uniqueness, or owner pagination contract is incomplete';
  END IF;

  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service')
    AND NOT has_table_privilege('lites_product_service','product.route_revisions','SELECT,INSERT,UPDATE') THEN
    RAISE EXCEPTION 'product service cannot operate route revisions';
  END IF;
END
$$;

SELECT json_build_object(
  'status','passed',
  'schema_version',60,
  'route_revision','planner_command_and_payload_integrity_bound'
) AS current_schema_verification;
