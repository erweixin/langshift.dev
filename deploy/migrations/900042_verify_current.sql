BEGIN;

DO $verify$
DECLARE source text;
BEGIN
  IF (SELECT count(*) FROM public.lites_schema_migrations)<>42
    OR NOT EXISTS (
      SELECT 1 FROM public.lites_schema_migrations
      WHERE version=42 AND name='runtime_run_cancellation_authority'
    )
  THEN
    RAISE EXCEPTION 'expected migration version 42';
  END IF;

  SELECT prosrc INTO source FROM pg_proc
  WHERE oid='agent.runtime_lock_cancelled_run_session(uuid,uuid,uuid,uuid,uuid,bigint,timestamp with time zone)'::regprocedure
    AND prosecdef
    AND 'row_security=off'=ANY(proconfig);
  IF source IS NULL
    OR position('c.store_epoch=p_store_epoch' IN source)=0
    OR position('ce.store_epoch=p_store_epoch' IN source)=0
    OR position('r.cancel_requested_at IS NOT NULL' IN source)=0
    OR position('c.status=''terminating''' IN source)=0
  THEN
    RAISE EXCEPTION 'runtime cancellation authority is not exact-run, epoch and cancellation fenced';
  END IF;
END
$verify$;

ROLLBACK;
SELECT json_build_object(
  'status','passed',
  'schema_version',42,
  'runtime_run_cancellation_authority','exact_run_epoch_and_cancellation_fenced'
) AS current_schema_verification;
