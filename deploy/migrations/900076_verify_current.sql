DO $$
DECLARE actual integer;
BEGIN
  SELECT max(version) INTO actual FROM public.lites_schema_migrations;
  IF actual<>76 OR NOT EXISTS (SELECT 1 FROM public.lites_schema_migrations WHERE version=76 AND name='enterprise_program_protocol')
  THEN RAISE EXCEPTION 'expected migration version 76'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='programs_contract')
    OR NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='cohorts_contract')
    OR NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='enrollments_contract')
    OR NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='role_packs_contract')
    OR NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='programs_lifecycle' AND NOT tgisinternal)
    OR NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='cohorts_lifecycle' AND NOT tgisinternal)
    OR NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='enrollments_lifecycle' AND NOT tgisinternal)
    OR NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='role_packs_append_only' AND NOT tgisinternal)
  THEN RAISE EXCEPTION 'enterprise program protocol verification failed'; END IF;
END
$$;

SELECT json_build_object('status','passed','schema_version',76,'enterprise_programs','tenant_scoped_cas_and_append_only_role_packs') AS current_schema_verification;
