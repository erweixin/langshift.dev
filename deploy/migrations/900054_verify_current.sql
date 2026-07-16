DO $$
DECLARE
  function_definition text;
  index_definition text;
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM public.lites_schema_migrations
    WHERE version=54 AND name='tool_effect_manual_review_routing'
  ) THEN
    RAISE EXCEPTION 'tool effect manual review routing migration is not installed';
  END IF;

  SELECT pg_get_functiondef('agent.list_expired_tool_effect_tenants(uuid,uuid,integer,integer,integer,timestamptz)'::regprocedure)
    INTO function_definition;
  SELECT indexdef FROM pg_indexes
    WHERE schemaname='agent' AND indexname='tool_calls_expired_effect_idx'
    INTO index_definition;

  IF position('idempotent_write' IN function_definition)=0
    OR position('reconcilable_write' IN function_definition)=0
    OR position('compensatable_write' IN function_definition)=0
    OR position('irreversible_write' IN function_definition)=0
    OR position('idempotent_write' IN index_definition)=0
    OR position('compensatable_write' IN index_definition)=0
    OR position('irreversible_write' IN index_definition)=0 THEN
    RAISE EXCEPTION 'expired effect recovery does not cover every write class';
  END IF;
END
$$;

SELECT json_build_object(
  'status','passed',
  'schema_version',54,
  'expired_effect_routing','automatic_reconciliation_or_durable_manual_review'
) AS current_schema_verification;
