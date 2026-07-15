\set ON_ERROR_STOP on
BEGIN;
DO $verify$
BEGIN
  IF (SELECT count(*) FROM public.lites_schema_migrations)<>34 OR NOT EXISTS (
    SELECT 1 FROM public.lites_schema_migrations WHERE version=34 AND name='runtime_boot_receipt'
  ) THEN RAISE EXCEPTION 'expected migration version 34'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='agent.runtime_sessions'::regclass AND conname='runtime_sessions_boot_receipt_contract')
    OR NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid='agent.runtime_sessions'::regclass AND tgname='runtime_boot_receipt_lifecycle')
  THEN RAISE EXCEPTION 'runtime boot receipt contract missing'; END IF;
END
$verify$;
ROLLBACK;
SELECT json_build_object('status','passed','schema_version',34,'runtime_boot_receipt','immutable_and_event_referenced') AS current_schema_verification;
