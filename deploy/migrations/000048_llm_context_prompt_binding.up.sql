DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM agent.llm_attempts
    WHERE jsonb_typeof(context_manifest->'prompt') IS DISTINCT FROM 'object'
      OR NULLIF(context_manifest->'prompt'->>'id','') IS NULL
      OR COALESCE(context_manifest->'prompt'->>'version','') !~ '^[1-9][0-9]*$'
      OR context_manifest->'prompt'->>'hash' !~ '^[0-9a-f]{64}$'
  ) THEN
    RAISE EXCEPTION 'LLM context prompt binding backfill required before migration 48' USING ERRCODE='55000';
  END IF;
END
$$;

ALTER TABLE agent.llm_attempts
  DROP CONSTRAINT llm_attempts_context_contract,
  ADD CONSTRAINT llm_attempts_context_contract CHECK (
    run_fence>0 AND stream_generation>0 AND NULLIF(attempt_key,'') IS NOT NULL
    AND jsonb_typeof(context_manifest)='object'
    AND jsonb_typeof(context_manifest->'prompt')='object'
    AND NULLIF(context_manifest->'prompt'->>'id','') IS NOT NULL
    AND context_manifest->'prompt'->>'version' ~ '^[1-9][0-9]*$'
    AND context_manifest->'prompt'->>'hash' ~ '^[0-9a-f]{64}$'
    AND jsonb_typeof(context_manifest->'candidate_models')='array'
    AND jsonb_array_length(context_manifest->'candidate_models')>0
    AND NULLIF(context_manifest_hash,'') IS NOT NULL AND NULLIF(router_snapshot_id,'') IS NOT NULL
    AND router_snapshot_id=context_manifest->'router'->>'id'
    AND total_input_tokens>=0 AND total_output_tokens>=0 AND total_cost_microunits>=0
  );
