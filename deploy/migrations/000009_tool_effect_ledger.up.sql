ALTER TABLE agent.tool_effects
  ADD COLUMN IF NOT EXISTS effect_class text,
  ADD COLUMN IF NOT EXISTS execution_attempt_id uuid,
  ADD COLUMN IF NOT EXISTS execution_fence bigint;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM agent.tool_effects WHERE effect_class IS NULL) THEN
    RAISE EXCEPTION 'tool effect v9 migration requires an explicit effect_class and execution-owner backfill before upgrade';
  END IF;
END
$$;

ALTER TABLE agent.tool_calls
  ADD CONSTRAINT tool_calls_tenant_id_id_run_unique UNIQUE (tenant_id,id,run_id);

ALTER TABLE agent.job_attempts
  ADD CONSTRAINT job_attempts_tenant_id_id_unique UNIQUE (tenant_id,id);

ALTER TABLE agent.tool_effects
  DROP CONSTRAINT tool_effects_result_event_fk;

ALTER TABLE agent.tool_effects
  ALTER COLUMN effect_class SET NOT NULL,
  ADD CONSTRAINT tool_effects_identity_contract CHECK (
    effect_scope<>'' AND provider_id<>'' AND tool_name<>'' AND effect_key<>'' AND request_hash<>''
    AND effect_class IN ('idempotent_write','reconcilable_write','compensatable_write','irreversible_write')
  ),
  ADD CONSTRAINT tool_effects_execution_owner_contract CHECK (
    (status IN ('executing','confirmed','outcome_unknown') AND provider_request_id IS NOT NULL AND provider_request_id<>'' AND execution_attempt_id IS NOT NULL AND execution_fence>0)
    OR (status IN ('prepared','commit_authorized','failed','accepted_unknown'))
  ),
  ADD CONSTRAINT tool_effects_shape_contract CHECK (
    (status='prepared' AND provider_request_id IS NULL AND execution_attempt_id IS NULL AND execution_fence IS NULL AND external_resource_ref IS NULL AND reconciliation_due_at IS NULL AND confirmed_at IS NULL AND result_event_id IS NULL AND residual_risk_ref IS NULL)
    OR (status='executing' AND reconciliation_due_at IS NULL AND confirmed_at IS NULL AND result_event_id IS NULL AND residual_risk_ref IS NULL)
    OR (status='commit_authorized' AND reconciliation_due_at IS NULL AND confirmed_at IS NULL AND result_event_id IS NULL AND residual_risk_ref IS NULL)
    OR (status='confirmed' AND confirmed_at IS NOT NULL AND result_event_id IS NOT NULL AND reconciliation_due_at IS NULL AND residual_risk_ref IS NULL)
    OR (status='failed' AND confirmed_at IS NULL AND result_event_id IS NOT NULL AND reconciliation_due_at IS NULL AND residual_risk_ref IS NULL)
    OR (status='outcome_unknown' AND confirmed_at IS NULL AND result_event_id IS NOT NULL AND reconciliation_due_at IS NOT NULL AND residual_risk_ref IS NULL)
    OR (status='accepted_unknown' AND confirmed_at IS NULL AND result_event_id IS NOT NULL AND reconciliation_due_at IS NULL AND residual_risk_ref IS NOT NULL)
  ),
  ADD CONSTRAINT tool_effects_tool_call_once UNIQUE (tenant_id,tool_call_id),
  ADD CONSTRAINT tool_effects_tenant_tool_run_fk FOREIGN KEY (tenant_id,tool_call_id,run_id)
    REFERENCES agent.tool_calls(tenant_id,id,run_id) ON DELETE CASCADE,
  ADD CONSTRAINT tool_effects_execution_attempt_fk FOREIGN KEY (tenant_id,execution_attempt_id)
    REFERENCES agent.job_attempts(tenant_id,id) DEFERRABLE INITIALLY DEFERRED,
  ADD CONSTRAINT tool_effects_result_event_fk FOREIGN KEY (result_event_id)
    REFERENCES agent.events(id) DEFERRABLE INITIALLY DEFERRED;

CREATE FUNCTION agent.enforce_tool_effect_lifecycle() RETURNS trigger
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

CREATE TRIGGER tool_effects_lifecycle
BEFORE UPDATE OR DELETE ON agent.tool_effects
FOR EACH ROW EXECUTE FUNCTION agent.enforce_tool_effect_lifecycle();
