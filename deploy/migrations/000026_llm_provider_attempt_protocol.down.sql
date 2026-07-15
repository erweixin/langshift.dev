DROP FUNCTION IF EXISTS agent.list_recoverable_llm_provider_tenants(uuid,uuid,integer,integer,integer,timestamptz);
DROP INDEX IF EXISTS agent.llm_provider_attempts_recovery_idx;
DROP TRIGGER IF EXISTS llm_provider_attempts_lifecycle ON agent.llm_provider_attempts;
DROP FUNCTION IF EXISTS agent.enforce_llm_provider_attempt_lifecycle();
DROP TRIGGER IF EXISTS llm_attempts_lifecycle ON agent.llm_attempts;
DROP FUNCTION IF EXISTS agent.enforce_llm_attempt_lifecycle();

ALTER TABLE agent.llm_attempts DROP CONSTRAINT IF EXISTS llm_attempts_selected_provider_fk;

ALTER TABLE agent.llm_provider_attempts
  DROP CONSTRAINT IF EXISTS llm_provider_attempts_reconciled_event_fk,
  DROP CONSTRAINT IF EXISTS llm_provider_attempts_recorded_event_fk,
  DROP CONSTRAINT IF EXISTS llm_provider_attempts_abandoned_event_fk,
  DROP CONSTRAINT IF EXISTS llm_provider_attempts_dispatch_event_fk,
  DROP CONSTRAINT IF EXISTS llm_provider_attempts_prepared_event_fk,
  DROP CONSTRAINT IF EXISTS llm_provider_attempts_byok_fk,
  DROP CONSTRAINT IF EXISTS llm_provider_attempts_fallback_fk,
  DROP CONSTRAINT IF EXISTS llm_provider_attempts_tenant_llm_fk,
  DROP CONSTRAINT IF EXISTS llm_provider_attempts_lifecycle_contract,
  DROP CONSTRAINT IF EXISTS llm_provider_attempts_scope_contract,
  DROP CONSTRAINT IF EXISTS llm_provider_attempts_status_contract,
  DROP CONSTRAINT IF EXISTS llm_provider_attempts_ordinal_unique,
  DROP CONSTRAINT IF EXISTS llm_provider_attempts_tenant_id_id_unique,
  DROP COLUMN IF EXISTS reconciled_event_id,
  DROP COLUMN IF EXISTS recorded_event_id,
  DROP COLUMN IF EXISTS abandoned_event_id,
  DROP COLUMN IF EXISTS dispatch_event_id,
  DROP COLUMN IF EXISTS prepared_event_id,
  DROP COLUMN IF EXISTS reconciliation_evidence_hash,
  DROP COLUMN IF EXISTS reconciled_at,
  DROP COLUMN IF EXISTS dispatched_at,
  DROP COLUMN IF EXISTS completion_deadline,
  DROP COLUMN IF EXISTS completion_token_hash,
  DROP COLUMN IF EXISTS prepare_token_expires_at,
  DROP COLUMN IF EXISTS prepare_token_hash,
  DROP COLUMN IF EXISTS dispatch_fence,
  DROP COLUMN IF EXISTS secret_version,
  DROP COLUMN IF EXISTS bound_host,
  DROP COLUMN IF EXISTS byok_credential_version,
  DROP COLUMN IF EXISTS byok_credential_id,
  DROP COLUMN IF EXISTS byok,
  DROP COLUMN IF EXISTS reconciliation_due_at,
  DROP COLUMN IF EXISTS visible_output_started_at,
  DROP COLUMN IF EXISTS error_class,
  DROP COLUMN IF EXISTS usage_status,
  DROP COLUMN IF EXISTS response_hash,
  DROP COLUMN IF EXISTS pricing_version,
  DROP COLUMN IF EXISTS context_manifest_hash,
  DROP COLUMN IF EXISTS request_hash,
  DROP COLUMN IF EXISTS ordinal;

ALTER TABLE agent.llm_provider_attempts ALTER COLUMN model_version DROP NOT NULL;

CREATE TRIGGER llm_provider_attempts_append_only BEFORE UPDATE OR DELETE ON agent.llm_provider_attempts
  FOR EACH ROW EXECUTE FUNCTION agent.reject_append_only_mutation();

ALTER TABLE agent.llm_attempts
  DROP CONSTRAINT IF EXISTS llm_attempts_finalized_event_fk,
  DROP CONSTRAINT IF EXISTS llm_attempts_started_event_fk,
  DROP CONSTRAINT IF EXISTS llm_attempts_tenant_run_fk,
  DROP CONSTRAINT IF EXISTS llm_attempts_lifecycle_contract,
  DROP CONSTRAINT IF EXISTS llm_attempts_context_contract,
  DROP CONSTRAINT IF EXISTS llm_attempts_status_contract,
  DROP CONSTRAINT IF EXISTS llm_attempts_run_stream_unique,
  DROP CONSTRAINT IF EXISTS llm_attempts_tenant_id_id_unique,
  DROP COLUMN IF EXISTS finalized_at,
  DROP COLUMN IF EXISTS finalized_event_id,
  DROP COLUMN IF EXISTS failure_code,
  DROP COLUMN IF EXISTS result_hash,
  DROP COLUMN IF EXISTS selected_provider_attempt_id,
  DROP COLUMN IF EXISTS started_event_id,
  DROP COLUMN IF EXISTS context_manifest_hash,
  DROP COLUMN IF EXISTS stream_generation,
  DROP COLUMN IF EXISTS run_fence,
  DROP COLUMN IF EXISTS run_attempt_id;

ALTER TABLE product.byok_credentials
  DROP CONSTRAINT IF EXISTS byok_credentials_current_version_fk,
  DROP CONSTRAINT IF EXISTS byok_credentials_scope_contract,
  DROP CONSTRAINT IF EXISTS byok_credentials_status_contract,
  DROP CONSTRAINT IF EXISTS byok_credentials_tenant_id_id_unique;

DROP TABLE IF EXISTS product.byok_credential_versions;
