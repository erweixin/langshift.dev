BEGIN;

DO $verify$
BEGIN
  IF (SELECT count(*) FROM public.lites_schema_migrations)<>52
    OR NOT EXISTS (SELECT 1 FROM public.lites_schema_migrations WHERE version=52 AND name='runtime_execution_ledger')
  THEN RAISE EXCEPTION 'expected migration version 52'; END IF;
  IF to_regclass('agent.runtime_executions') IS NULL
    OR NOT EXISTS (SELECT 1 FROM pg_class WHERE oid='agent.runtime_executions'::regclass AND relrowsecurity AND relforcerowsecurity)
    OR NOT EXISTS (SELECT 1 FROM pg_policies WHERE schemaname='agent' AND tablename='runtime_executions' AND policyname='runtime_executions_tenant_isolation')
    OR NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid='agent.runtime_executions'::regclass AND tgname='runtime_execution_lifecycle' AND tgenabled='O')
    OR NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid='agent.runtime_executions'::regclass AND tgname='runtime_execution_event_guard' AND tgdeferrable AND tginitdeferred)
    OR NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid='agent.runtime_sessions'::regclass AND tgname='runtime_session_execution_settlement_guard' AND tgdeferrable AND tginitdeferred)
    OR NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='agent.runtime_executions'::regclass AND conname='runtime_executions_lifecycle_contract')
    OR NOT EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname='agent' AND indexname='runtime_executions_one_running_per_session')
  THEN RAISE EXCEPTION 'runtime execution ledger contract is incomplete'; END IF;
END
$verify$;

ROLLBACK;
SELECT json_build_object('status','passed','schema_version',52,'runtime_execution_ledger','atomic_session_execution_evidence') AS current_schema_verification;
