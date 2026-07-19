DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid='product.submission_review_generations'::regclass
      AND conname='submission_review_generations_submission_unique'
  ) OR NOT EXISTS (
    SELECT 1 FROM pg_indexes
    WHERE schemaname='product'
      AND tablename='submission_review_generations'
      AND indexname='submission_review_generations_active_submission_unique'
      AND indexdef LIKE '%WHERE (status = ANY%generating%succeeded%'
  ) OR position(
    'NOT EXISTS' IN pg_get_functiondef(
      'agent.lock_owned_submission_review(uuid,uuid,uuid,integer,uuid,bigint)'::regprocedure
    )
  ) = 0 THEN
    RAISE EXCEPTION 'Review generation retry boundary is incomplete';
  END IF;
END
$$;
