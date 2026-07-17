DO $$ BEGIN
  IF (SELECT max(version) FROM public.lites_schema_migrations)<>69 THEN RAISE EXCEPTION 'expected migration version 69'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='coach_context_snapshots_run_unique') THEN RAISE EXCEPTION 'Coach context Run fence missing'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_proc WHERE proname='read_coach_context_snapshot') THEN RAISE EXCEPTION 'Coach context scoped reader missing'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_proc WHERE proname='lock_coach_context_fence') THEN RAISE EXCEPTION 'Coach context commit fence missing'; END IF;
END $$;
SELECT json_build_object('status','passed','schema_version',69,'coach_context','immutable_content_addressed_focus_fenced') AS current_schema_verification;
