BEGIN;

DO $verify$
BEGIN
  IF (SELECT count(*) FROM public.lites_schema_migrations)<>47
    OR NOT EXISTS (SELECT 1 FROM public.lites_schema_migrations WHERE version=47 AND name='run_context_source_protocol')
  THEN RAISE EXCEPTION 'expected migration version 47'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='run_messages_append_only' AND tgenabled='O')
    OR NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='run_messages_scope_contract')
    OR NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='run_messages_tenant_run_fk')
    OR NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='run_messages_finalized_event_fk' AND condeferrable AND condeferred)
    OR NOT EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname='agent' AND indexname='run_messages_context_idx')
  THEN RAISE EXCEPTION 'run context-source contract is incomplete'; END IF;
END
$verify$;

ROLLBACK;
SELECT json_build_object('status','passed','schema_version',47,'run_messages','immutable_trust_labeled_context_sources') AS current_schema_verification;
