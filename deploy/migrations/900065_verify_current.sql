DO $$
DECLARE actual integer;
BEGIN
  SELECT max(version) INTO actual FROM public.lites_schema_migrations;
  IF actual<>65 THEN RAISE EXCEPTION 'expected migration version 65'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='submission_review_generations_lifecycle_contract') THEN
    RAISE EXCEPTION 'submission review lifecycle fence missing';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='reviews_evidence_fk') THEN
    RAISE EXCEPTION 'review evidence exact binding missing';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_proc WHERE proname='lock_owned_submission_review') THEN
    RAISE EXCEPTION 'submission review admission lock missing';
  END IF;
END
$$;

SELECT json_build_object('status','passed','schema_version',65,'submission_review','pinned_evaluator_run','evidence','exact_immutable_binding') AS current_schema_verification;
