BEGIN;

DO $verify$
BEGIN
  IF (SELECT count(*) FROM public.lites_schema_migrations)<>45
    OR NOT EXISTS (SELECT 1 FROM public.lites_schema_migrations WHERE version=45 AND name='child_group_remainder_cancellation')
  THEN RAISE EXCEPTION 'expected migration version 45'; END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema='agent' AND table_name='child_group_cancellations')
    OR NOT EXISTS (SELECT 1 FROM pg_policies WHERE schemaname='agent' AND tablename='child_group_cancellations')
    OR NOT EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname='agent' AND indexname='child_group_cancellations_due_idx')
    OR NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='child_group_cancellation_lifecycle' AND tgenabled='O')
  THEN RAISE EXCEPTION 'child group remainder cancellation contract is incomplete'; END IF;
END
$verify$;

ROLLBACK;
SELECT json_build_object(
  'status','passed','schema_version',45,
  'child_group_remainder_cancellation','durable_cursor_fenced_cleanup'
) AS current_schema_verification;
