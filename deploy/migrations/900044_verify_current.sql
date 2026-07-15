BEGIN;

DO $verify$
BEGIN
  IF (SELECT count(*) FROM public.lites_schema_migrations)<>44
    OR NOT EXISTS (SELECT 1 FROM public.lites_schema_migrations WHERE version=44 AND name='run_cancellation_propagation')
  THEN RAISE EXCEPTION 'expected migration version 44'; END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='agent' AND table_name='run_cancellations' AND column_name='propagation_cursor')
    OR NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='agent' AND table_name='run_cancellations' AND column_name='attempt_cancelled_payload_ref')
    OR NOT EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname='agent' AND indexname='run_cancellations_propagation_idx')
  THEN RAISE EXCEPTION 'run cancellation propagation contract is incomplete'; END IF;
END
$verify$;

ROLLBACK;
SELECT json_build_object(
  'status','passed','schema_version',44,
  'run_cancellation_propagation','cursor_fenced_recursive_cancellation'
) AS current_schema_verification;
