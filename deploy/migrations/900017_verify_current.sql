\set ON_ERROR_STOP on
BEGIN;
DO $verify$
DECLARE actual integer; lifecycle text; scope_contract text;
BEGIN
  SELECT count(*) INTO actual FROM public.lites_schema_migrations;
  IF actual<>17 OR NOT EXISTS (SELECT 1 FROM public.lites_schema_migrations WHERE version=17 AND name='repair_evidence_expiry') THEN RAISE EXCEPTION 'expected migration version 17'; END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='agent' AND table_name='repair_commands' AND column_name='evidence_payload_ref') THEN RAISE EXCEPTION 'repair evidence payload reference missing'; END IF;
  SELECT pg_get_constraintdef(oid) INTO scope_contract FROM pg_constraint WHERE conrelid='agent.repair_commands'::regclass AND conname='repair_commands_scope_contract';
  IF position('evidence_payload_ref' IN scope_contract)=0 THEN RAISE EXCEPTION 'claim repair evidence reference fence missing'; END IF;
  SELECT pg_get_functiondef('agent.enforce_repair_command_lifecycle()'::regprocedure) INTO lifecycle;
  IF position('evidence_payload_ref' IN lifecycle)=0 THEN RAISE EXCEPTION 'repair evidence immutability fence missing'; END IF;
  IF to_regprocedure('agent.list_expired_repair_tenants(uuid,uuid,integer,integer,integer,timestamp with time zone)') IS NULL THEN RAISE EXCEPTION 'repair expiry scan missing'; END IF;
END
$verify$;
ROLLBACK;
SELECT json_build_object('status','passed','schema_version',17,'repair_evidence','encrypted_reference','repair_expiry','epoch_fenced') AS current_schema_verification;
