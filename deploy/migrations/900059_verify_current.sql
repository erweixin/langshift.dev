DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM public.lites_schema_migrations
    WHERE version=59 AND name='mission_goal_payload_integrity'
  ) THEN
    RAISE EXCEPTION 'mission goal payload integrity migration is not installed';
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='product' AND table_name='missions'
      AND column_name='goal_payload_hash' AND data_type='text'
  ) OR NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conname='missions_goal_payload_integrity' AND contype='c'
  ) THEN
    RAISE EXCEPTION 'mission goal payload integrity columns or constraint are missing';
  END IF;

  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service')
    AND NOT has_table_privilege('lites_product_service','product.missions','SELECT,INSERT,UPDATE') THEN
    RAISE EXCEPTION 'product service cannot operate mission goal payload manifests';
  END IF;
END
$$;

SELECT json_build_object(
  'status','passed',
  'schema_version',59,
  'mission_goal_payload','content_addressed_ref_hash_pair'
) AS current_schema_verification;
