BEGIN;

DO $verify$
DECLARE definition text;
BEGIN
  IF (SELECT count(*) FROM public.lites_schema_migrations)<>48
    OR NOT EXISTS (SELECT 1 FROM public.lites_schema_migrations WHERE version=48 AND name='llm_context_prompt_binding')
  THEN RAISE EXCEPTION 'expected migration version 48'; END IF;
  SELECT pg_get_constraintdef(oid) INTO definition FROM pg_constraint WHERE conname='llm_attempts_context_contract';
  IF position('prompt' IN definition)=0 OR position('router_snapshot_id' IN definition)=0 THEN
    RAISE EXCEPTION 'LLM context prompt/router binding is incomplete';
  END IF;
END
$verify$;

ROLLBACK;
SELECT json_build_object('status','passed','schema_version',48,'context_manifest','explicit_prompt_and_router_binding') AS current_schema_verification;
