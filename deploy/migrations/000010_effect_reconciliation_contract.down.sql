CREATE OR REPLACE FUNCTION agent.enforce_tool_effect_lifecycle() RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'tool effect deletion is forbidden'; END IF;
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.created_at IS DISTINCT FROM OLD.created_at OR NEW.effect_scope IS DISTINCT FROM OLD.effect_scope
    OR NEW.provider_id IS DISTINCT FROM OLD.provider_id OR NEW.tool_name IS DISTINCT FROM OLD.tool_name
    OR NEW.effect_key IS DISTINCT FROM OLD.effect_key OR NEW.request_hash IS DISTINCT FROM OLD.request_hash
    OR NEW.tool_call_id IS DISTINCT FROM OLD.tool_call_id OR NEW.run_id IS DISTINCT FROM OLD.run_id
    OR NEW.effect_class IS DISTINCT FROM OLD.effect_class THEN
    RAISE EXCEPTION 'immutable tool effect identity mutation is forbidden';
  END IF;
  IF NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at THEN
    RAISE EXCEPTION 'tool effect version must advance exactly once';
  END IF;
  IF OLD.provider_request_id IS NOT NULL AND NEW.provider_request_id IS DISTINCT FROM OLD.provider_request_id THEN
    RAISE EXCEPTION 'tool effect provider request identity mutation is forbidden';
  END IF;
  IF OLD.external_resource_ref IS NOT NULL AND NEW.external_resource_ref IS DISTINCT FROM OLD.external_resource_ref THEN
    RAISE EXCEPTION 'tool effect external resource mutation is forbidden';
  END IF;
  IF OLD.result_event_id IS NOT NULL AND NEW.result_event_id IS DISTINCT FROM OLD.result_event_id THEN
    RAISE EXCEPTION 'tool effect result event mutation is forbidden';
  END IF;
  IF NOT (
    (OLD.status='prepared' AND NEW.status IN ('executing','commit_authorized','failed'))
    OR (OLD.status='executing' AND NEW.status IN ('confirmed','failed','outcome_unknown'))
    OR (OLD.status='executing' AND NEW.status='executing' AND OLD.effect_class='idempotent_write'
      AND NEW.execution_attempt_id IS DISTINCT FROM OLD.execution_attempt_id AND NEW.execution_fence>OLD.execution_fence)
    OR (OLD.status='commit_authorized' AND NEW.status IN ('executing','failed'))
    OR (OLD.status='outcome_unknown' AND NEW.status IN ('confirmed','failed','commit_authorized','accepted_unknown'))
  ) THEN
    RAISE EXCEPTION 'invalid tool effect lifecycle transition';
  END IF;
  RETURN NEW;
END
$$;
