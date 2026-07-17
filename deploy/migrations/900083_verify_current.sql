DO $$
BEGIN
  IF to_regclass('product.task_packs') IS NULL
    OR NOT EXISTS (SELECT 1 FROM pg_class WHERE oid='product.task_packs'::regclass AND relrowsecurity AND relforcerowsecurity)
    OR NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid='product.task_packs'::regclass AND tgname='task_packs_append_only' AND NOT tgisinternal)
    OR NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='task_packs_contract')
  THEN RAISE EXCEPTION 'task pack control plane verification failed'; END IF;
END
$$;
SELECT json_build_object('status','passed','schema_version',83,'task_packs','tenant_scoped_append_only') AS current_schema_verification;
