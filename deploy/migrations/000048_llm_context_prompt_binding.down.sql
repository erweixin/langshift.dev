ALTER TABLE agent.llm_attempts
  DROP CONSTRAINT llm_attempts_context_contract,
  ADD CONSTRAINT llm_attempts_context_contract CHECK (
    run_fence>0 AND stream_generation>0 AND NULLIF(attempt_key,'') IS NOT NULL
    AND jsonb_typeof(context_manifest)='object'
    AND jsonb_typeof(context_manifest->'candidate_models')='array'
    AND jsonb_array_length(context_manifest->'candidate_models')>0
    AND NULLIF(context_manifest_hash,'') IS NOT NULL AND NULLIF(router_snapshot_id,'') IS NOT NULL
    AND total_input_tokens>=0 AND total_output_tokens>=0 AND total_cost_microunits>=0
  );
