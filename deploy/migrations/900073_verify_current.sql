DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
    WHERE n.nspname='agent' AND p.proname='lock_authorized_artifact_export'
  ) THEN
    RAISE EXCEPTION 'artifact export tool admission protocol is incomplete';
  END IF;
END
$$;
