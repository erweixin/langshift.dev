\set ON_ERROR_STOP on
BEGIN;
DO $verify$
DECLARE actual integer; llm_lifecycle text; provider_lifecycle text;
BEGIN
  SELECT count(*) INTO actual FROM public.lites_schema_migrations;
  IF actual<>26 OR NOT EXISTS (SELECT 1 FROM public.lites_schema_migrations WHERE version=26 AND name='llm_provider_attempt_protocol') THEN RAISE EXCEPTION 'expected migration version 26'; END IF;
  SELECT pg_get_functiondef('agent.enforce_llm_attempt_lifecycle()'::regprocedure) INTO llm_lifecycle;
  SELECT pg_get_functiondef('agent.enforce_llm_provider_attempt_lifecycle()'::regprocedure) INTO provider_lifecycle;
  IF position('immutable LLM attempt scope mutation is forbidden' IN llm_lifecycle)=0 THEN RAISE EXCEPTION 'immutable context manifest enforcement is absent'; END IF;
  IF position('prepared' IN provider_lifecycle)=0 OR position('dispatching' IN provider_lifecycle)=0 OR position('outcome_unknown' IN provider_lifecycle)=0 THEN RAISE EXCEPTION 'provider attempt state machine is incomplete'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='llm_provider_attempts_byok_fk') THEN RAISE EXCEPTION 'exact BYOK version binding is absent'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='byok_credential_versions_append_only' AND NOT tgisinternal) THEN RAISE EXCEPTION 'immutable BYOK version history is absent'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='byok_credentials_current_version_fk') THEN RAISE EXCEPTION 'BYOK current pointer is not version bound'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='llm_provider_attempts_ordinal_unique') THEN RAISE EXCEPTION 'physical fallback ordinal uniqueness is absent'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='llm_attempts_selected_provider_fk') THEN RAISE EXCEPTION 'selected physical attempt binding is absent'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_proc WHERE oid='agent.list_recoverable_llm_provider_tenants(uuid,uuid,integer,integer,integer,timestamptz)'::regprocedure AND prosecdef) THEN RAISE EXCEPTION 'RLS-safe provider recovery discovery is absent'; END IF;
END
$verify$;
ROLLBACK;
SELECT json_build_object('status','passed','schema_version',26,'llm_provider_attempts','one_shot_dispatch_exact_byok_reconcilable') AS current_schema_verification;
