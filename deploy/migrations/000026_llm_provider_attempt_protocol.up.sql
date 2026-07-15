DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM agent.llm_attempts)
    OR EXISTS (SELECT 1 FROM agent.llm_provider_attempts) THEN
    RAISE EXCEPTION 'LLM attempt protocol backfill required before migration 26' USING ERRCODE='55000';
  END IF;
END
$$;

ALTER TABLE product.byok_credentials
  ADD CONSTRAINT byok_credentials_tenant_id_id_unique UNIQUE (tenant_id,id),
  ADD CONSTRAINT byok_credentials_status_contract CHECK (status IN ('pending_validation','active','invalid','revoked')),
  ADD CONSTRAINT byok_credentials_scope_contract CHECK (
    NULLIF(provider_id,'') IS NOT NULL AND NULLIF(bound_host,'') IS NOT NULL
    AND bound_host=lower(bound_host) AND bound_host !~ '[/@:]'
    AND NULLIF(secret_ref,'') IS NOT NULL AND NULLIF(secret_version,'') IS NOT NULL
  );

CREATE TABLE product.byok_credential_versions (
  tenant_id uuid NOT NULL,
  credential_id uuid NOT NULL,
  version bigint NOT NULL,
  user_id uuid NOT NULL,
  provider_id text NOT NULL,
  bound_host text NOT NULL,
  secret_ref text NOT NULL,
  secret_version text NOT NULL,
  status text NOT NULL,
  last_validated_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (tenant_id,credential_id,version),
  CONSTRAINT byok_credential_versions_exact_unique UNIQUE (tenant_id,credential_id,version,bound_host,secret_version),
  CONSTRAINT byok_credential_versions_scope_contract CHECK (
    version>0 AND NULLIF(provider_id,'') IS NOT NULL AND NULLIF(bound_host,'') IS NOT NULL
    AND bound_host=lower(bound_host) AND bound_host !~ '[/@:]'
    AND NULLIF(secret_ref,'') IS NOT NULL AND NULLIF(secret_version,'') IS NOT NULL
    AND status IN ('pending_validation','active','invalid','revoked')
  ),
  CONSTRAINT byok_credential_versions_tenant_fk FOREIGN KEY (tenant_id)
    REFERENCES identity.tenants(id) ON DELETE RESTRICT
);

INSERT INTO product.byok_credential_versions(
  tenant_id,credential_id,version,user_id,provider_id,bound_host,secret_ref,secret_version,status,last_validated_at,created_at
)
SELECT tenant_id,id,version,user_id,provider_id,bound_host,secret_ref,secret_version,status,last_validated_at,created_at
FROM product.byok_credentials;

ALTER TABLE product.byok_credentials
  ADD CONSTRAINT byok_credentials_current_version_fk FOREIGN KEY (tenant_id,id,version,bound_host,secret_version)
    REFERENCES product.byok_credential_versions(tenant_id,credential_id,version,bound_host,secret_version) ON DELETE RESTRICT;

ALTER TABLE product.byok_credential_versions ENABLE ROW LEVEL SECURITY;
ALTER TABLE product.byok_credential_versions FORCE ROW LEVEL SECURITY;
CREATE POLICY byok_credential_versions_tenant_isolation ON product.byok_credential_versions
  USING (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid);
CREATE TRIGGER byok_credential_versions_append_only BEFORE UPDATE OR DELETE ON product.byok_credential_versions
  FOR EACH ROW EXECUTE FUNCTION agent.reject_append_only_mutation();

ALTER TABLE agent.llm_attempts
  ADD COLUMN run_attempt_id uuid,
  ADD COLUMN run_fence bigint,
  ADD COLUMN stream_generation bigint,
  ADD COLUMN context_manifest_hash text,
  ADD COLUMN started_event_id uuid,
  ADD COLUMN selected_provider_attempt_id uuid,
  ADD COLUMN result_hash text,
  ADD COLUMN failure_code text,
  ADD COLUMN finalized_event_id uuid,
  ADD COLUMN finalized_at timestamptz;

ALTER TABLE agent.llm_attempts
  ALTER COLUMN run_attempt_id SET NOT NULL,
  ALTER COLUMN run_fence SET NOT NULL,
  ALTER COLUMN stream_generation SET NOT NULL,
  ALTER COLUMN context_manifest_hash SET NOT NULL,
  ALTER COLUMN started_event_id SET NOT NULL,
  ADD CONSTRAINT llm_attempts_tenant_id_id_unique UNIQUE (tenant_id,id),
  ADD CONSTRAINT llm_attempts_run_stream_unique UNIQUE (tenant_id,run_id,stream_generation),
  ADD CONSTRAINT llm_attempts_status_contract CHECK (status IN ('running','completed','failed','partial_visible','cancelled')),
  ADD CONSTRAINT llm_attempts_context_contract CHECK (
    run_fence>0 AND stream_generation>0 AND NULLIF(attempt_key,'') IS NOT NULL
    AND jsonb_typeof(context_manifest)='object'
    AND jsonb_typeof(context_manifest->'candidate_models')='array'
    AND jsonb_array_length(context_manifest->'candidate_models')>0
    AND NULLIF(context_manifest_hash,'') IS NOT NULL AND NULLIF(router_snapshot_id,'') IS NOT NULL
    AND total_input_tokens>=0 AND total_output_tokens>=0 AND total_cost_microunits>=0
  ),
  ADD CONSTRAINT llm_attempts_lifecycle_contract CHECK (
    (status='running' AND version=1 AND selected_provider_attempt_id IS NULL AND result_hash IS NULL
      AND failure_code IS NULL AND finalized_event_id IS NULL AND finalized_at IS NULL
      AND total_input_tokens=0 AND total_output_tokens=0 AND total_cost_microunits=0)
    OR (status='completed' AND version=2 AND selected_provider_attempt_id IS NOT NULL
      AND NULLIF(result_hash,'') IS NOT NULL AND failure_code IS NULL
      AND finalized_event_id IS NOT NULL AND finalized_at IS NOT NULL)
    OR (status='partial_visible' AND version=2 AND selected_provider_attempt_id IS NOT NULL
      AND NULLIF(result_hash,'') IS NOT NULL AND NULLIF(failure_code,'') IS NOT NULL
      AND finalized_event_id IS NOT NULL AND finalized_at IS NOT NULL)
    OR (status IN ('failed','cancelled') AND version=2 AND selected_provider_attempt_id IS NULL
      AND result_hash IS NULL AND NULLIF(failure_code,'') IS NOT NULL
      AND finalized_event_id IS NOT NULL AND finalized_at IS NOT NULL)
  ),
  ADD CONSTRAINT llm_attempts_tenant_run_fk FOREIGN KEY (tenant_id,run_id)
    REFERENCES agent.runs(tenant_id,id) ON DELETE RESTRICT,
  ADD CONSTRAINT llm_attempts_started_event_fk FOREIGN KEY (started_event_id)
    REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  ADD CONSTRAINT llm_attempts_finalized_event_fk FOREIGN KEY (finalized_event_id)
    REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;

DROP TRIGGER IF EXISTS llm_provider_attempts_append_only ON agent.llm_provider_attempts;

ALTER TABLE agent.llm_provider_attempts
  ADD COLUMN ordinal integer,
  ADD COLUMN request_hash text,
  ADD COLUMN context_manifest_hash text,
  ADD COLUMN pricing_version text,
  ADD COLUMN response_hash text,
  ADD COLUMN usage_status text,
  ADD COLUMN error_class text,
  ADD COLUMN visible_output_started_at timestamptz,
  ADD COLUMN reconciliation_due_at timestamptz,
  ADD COLUMN byok boolean,
  ADD COLUMN byok_credential_id uuid,
  ADD COLUMN byok_credential_version bigint,
  ADD COLUMN bound_host text,
  ADD COLUMN secret_version text,
  ADD COLUMN dispatch_fence bigint NOT NULL DEFAULT 0,
  ADD COLUMN prepare_token_hash bytea,
  ADD COLUMN prepare_token_expires_at timestamptz,
  ADD COLUMN completion_token_hash bytea,
  ADD COLUMN completion_deadline timestamptz,
  ADD COLUMN dispatched_at timestamptz,
  ADD COLUMN reconciled_at timestamptz,
  ADD COLUMN reconciliation_evidence_hash text,
  ADD COLUMN prepared_event_id uuid,
  ADD COLUMN dispatch_event_id uuid,
  ADD COLUMN abandoned_event_id uuid,
  ADD COLUMN recorded_event_id uuid,
  ADD COLUMN reconciled_event_id uuid;

ALTER TABLE agent.llm_provider_attempts
  ALTER COLUMN ordinal SET NOT NULL,
  ALTER COLUMN request_hash SET NOT NULL,
  ALTER COLUMN context_manifest_hash SET NOT NULL,
  ALTER COLUMN pricing_version SET NOT NULL,
  ALTER COLUMN usage_status SET NOT NULL,
  ALTER COLUMN byok SET NOT NULL,
  ALTER COLUMN bound_host SET NOT NULL,
  ALTER COLUMN prepare_token_expires_at SET NOT NULL,
  ALTER COLUMN prepared_event_id SET NOT NULL,
  ALTER COLUMN model_version SET NOT NULL,
  ADD CONSTRAINT llm_provider_attempts_tenant_id_id_unique UNIQUE (tenant_id,id),
  ADD CONSTRAINT llm_provider_attempts_ordinal_unique UNIQUE (tenant_id,llm_attempt_id,ordinal),
  ADD CONSTRAINT llm_provider_attempts_status_contract CHECK (
    status IN ('prepared','dispatching','abandoned','completed','failed','cancelled','outcome_unknown')
  ),
  ADD CONSTRAINT llm_provider_attempts_scope_contract CHECK (
    ordinal>0 AND NULLIF(provider_attempt_id,'') IS NOT NULL AND NULLIF(provider_id,'') IS NOT NULL
    AND NULLIF(model_id,'') IS NOT NULL AND NULLIF(model_version,'') IS NOT NULL
    AND NULLIF(request_hash,'') IS NOT NULL AND NULLIF(context_manifest_hash,'') IS NOT NULL
    AND NULLIF(pricing_version,'') IS NOT NULL AND NULLIF(bound_host,'') IS NOT NULL
    AND bound_host=lower(bound_host) AND bound_host !~ '[/@:]'
    AND input_tokens>=0 AND output_tokens>=0 AND cost_microunits>=0 AND dispatch_fence>=0
    AND ((byok AND byok_credential_id IS NOT NULL AND byok_credential_version>0 AND NULLIF(secret_version,'') IS NOT NULL)
      OR (NOT byok AND byok_credential_id IS NULL AND byok_credential_version IS NULL AND secret_version IS NULL))
  ),
  ADD CONSTRAINT llm_provider_attempts_lifecycle_contract CHECK (
    (status='prepared' AND version=1 AND dispatch_fence=0 AND octet_length(prepare_token_hash)=32
      AND prepare_token_expires_at>started_at AND completion_token_hash IS NULL AND completion_deadline IS NULL
      AND dispatched_at IS NULL AND finished_at IS NULL AND response_hash IS NULL AND usage_status='pending'
      AND provider_request_id IS NULL AND error_class IS NULL AND visible_output_started_at IS NULL
      AND reconciliation_due_at IS NULL AND reconciled_at IS NULL AND reconciliation_evidence_hash IS NULL
      AND dispatch_event_id IS NULL AND abandoned_event_id IS NULL AND recorded_event_id IS NULL AND reconciled_event_id IS NULL
      AND input_tokens=0 AND output_tokens=0 AND cost_microunits=0)
    OR (status='dispatching' AND version=2 AND dispatch_fence=1 AND prepare_token_hash IS NULL
      AND octet_length(completion_token_hash)=32 AND completion_deadline>dispatched_at AND dispatched_at IS NOT NULL
      AND finished_at IS NULL AND response_hash IS NULL AND usage_status='pending' AND provider_request_id IS NULL
      AND error_class IS NULL AND reconciliation_due_at IS NULL AND reconciled_at IS NULL
      AND reconciliation_evidence_hash IS NULL AND dispatch_event_id IS NOT NULL
      AND abandoned_event_id IS NULL AND recorded_event_id IS NULL AND reconciled_event_id IS NULL
      AND input_tokens=0 AND output_tokens=0 AND cost_microunits=0)
    OR (status='abandoned' AND version=2 AND dispatch_fence=0 AND prepare_token_hash IS NULL
      AND completion_token_hash IS NULL AND completion_deadline IS NULL AND dispatched_at IS NULL
      AND finished_at IS NOT NULL AND response_hash IS NULL AND usage_status='unavailable'
      AND provider_request_id IS NULL AND NULLIF(error_class,'') IS NOT NULL
      AND visible_output_started_at IS NULL AND reconciliation_due_at IS NULL AND reconciled_at IS NULL
      AND reconciliation_evidence_hash IS NULL AND dispatch_event_id IS NULL
      AND abandoned_event_id IS NOT NULL AND recorded_event_id IS NULL AND reconciled_event_id IS NULL
      AND input_tokens=0 AND output_tokens=0 AND cost_microunits=0)
    OR (status IN ('completed','failed','cancelled') AND version=3 AND dispatch_fence=1
      AND prepare_token_hash IS NULL AND completion_token_hash IS NULL AND dispatched_at IS NOT NULL
      AND finished_at IS NOT NULL AND usage_status IN ('confirmed','estimated','unavailable')
      AND reconciliation_due_at IS NULL AND reconciled_at IS NULL AND reconciliation_evidence_hash IS NULL
      AND dispatch_event_id IS NOT NULL AND abandoned_event_id IS NULL
      AND recorded_event_id IS NOT NULL AND reconciled_event_id IS NULL
      AND ((status='completed' AND NULLIF(response_hash,'') IS NOT NULL AND error_class IS NULL)
        OR (status IN ('failed','cancelled') AND NULLIF(error_class,'') IS NOT NULL)))
    OR (status='outcome_unknown' AND version=3 AND dispatch_fence=1
      AND prepare_token_hash IS NULL AND completion_token_hash IS NULL AND dispatched_at IS NOT NULL
      AND finished_at IS NOT NULL AND response_hash IS NULL AND usage_status='unknown'
      AND NULLIF(error_class,'') IS NOT NULL AND reconciliation_due_at>finished_at
      AND reconciled_at IS NULL AND reconciliation_evidence_hash IS NULL
      AND dispatch_event_id IS NOT NULL AND abandoned_event_id IS NULL
      AND recorded_event_id IS NOT NULL AND reconciled_event_id IS NULL)
    OR (status='outcome_unknown' AND version=4 AND dispatch_fence=1
      AND prepare_token_hash IS NULL AND completion_token_hash IS NULL AND dispatched_at IS NOT NULL
      AND finished_at IS NOT NULL AND response_hash IS NULL AND usage_status='confirmed'
      AND NULLIF(error_class,'') IS NOT NULL AND reconciliation_due_at IS NOT NULL
      AND reconciled_at IS NOT NULL AND NULLIF(reconciliation_evidence_hash,'') IS NOT NULL
      AND dispatch_event_id IS NOT NULL AND abandoned_event_id IS NULL
      AND recorded_event_id IS NOT NULL AND reconciled_event_id IS NOT NULL)
  ),
  ADD CONSTRAINT llm_provider_attempts_tenant_llm_fk FOREIGN KEY (tenant_id,llm_attempt_id)
    REFERENCES agent.llm_attempts(tenant_id,id) ON DELETE RESTRICT,
  ADD CONSTRAINT llm_provider_attempts_fallback_fk FOREIGN KEY (tenant_id,fallback_from_id)
    REFERENCES agent.llm_provider_attempts(tenant_id,id) ON DELETE RESTRICT,
  ADD CONSTRAINT llm_provider_attempts_byok_fk FOREIGN KEY (tenant_id,byok_credential_id,byok_credential_version,bound_host,secret_version)
    REFERENCES product.byok_credential_versions(tenant_id,credential_id,version,bound_host,secret_version) ON DELETE RESTRICT,
  ADD CONSTRAINT llm_provider_attempts_prepared_event_fk FOREIGN KEY (prepared_event_id)
    REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  ADD CONSTRAINT llm_provider_attempts_dispatch_event_fk FOREIGN KEY (dispatch_event_id)
    REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  ADD CONSTRAINT llm_provider_attempts_abandoned_event_fk FOREIGN KEY (abandoned_event_id)
    REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  ADD CONSTRAINT llm_provider_attempts_recorded_event_fk FOREIGN KEY (recorded_event_id)
    REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  ADD CONSTRAINT llm_provider_attempts_reconciled_event_fk FOREIGN KEY (reconciled_event_id)
    REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE agent.llm_attempts
  ADD CONSTRAINT llm_attempts_selected_provider_fk FOREIGN KEY (tenant_id,selected_provider_attempt_id)
    REFERENCES agent.llm_provider_attempts(tenant_id,id) ON DELETE RESTRICT;

CREATE FUNCTION agent.enforce_llm_attempt_lifecycle() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'LLM attempt deletion is forbidden'; END IF;
  IF OLD.status<>'running' THEN RAISE EXCEPTION 'terminal LLM attempt mutation is forbidden'; END IF;
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.user_id IS DISTINCT FROM OLD.user_id OR NEW.created_at IS DISTINCT FROM OLD.created_at
    OR NEW.run_id IS DISTINCT FROM OLD.run_id OR NEW.run_attempt_id IS DISTINCT FROM OLD.run_attempt_id
    OR NEW.run_fence IS DISTINCT FROM OLD.run_fence OR NEW.attempt_key IS DISTINCT FROM OLD.attempt_key
    OR NEW.stream_generation IS DISTINCT FROM OLD.stream_generation
    OR NEW.context_manifest IS DISTINCT FROM OLD.context_manifest
    OR NEW.context_manifest_hash IS DISTINCT FROM OLD.context_manifest_hash
    OR NEW.router_snapshot_id IS DISTINCT FROM OLD.router_snapshot_id
    OR NEW.started_event_id IS DISTINCT FROM OLD.started_event_id THEN
    RAISE EXCEPTION 'immutable LLM attempt scope mutation is forbidden';
  END IF;
  IF NEW.version<>2 OR OLD.version<>1 OR NEW.updated_at<OLD.updated_at OR NEW.status='running' THEN
    RAISE EXCEPTION 'invalid LLM attempt finalization';
  END IF;
  RETURN NEW;
END
$$;

CREATE TRIGGER llm_attempts_lifecycle BEFORE UPDATE OR DELETE ON agent.llm_attempts
  FOR EACH ROW EXECUTE FUNCTION agent.enforce_llm_attempt_lifecycle();

CREATE FUNCTION agent.enforce_llm_provider_attempt_lifecycle() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'provider attempt deletion is forbidden'; END IF;
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.user_id IS DISTINCT FROM OLD.user_id OR NEW.created_at IS DISTINCT FROM OLD.created_at
    OR NEW.llm_attempt_id IS DISTINCT FROM OLD.llm_attempt_id OR NEW.provider_attempt_id IS DISTINCT FROM OLD.provider_attempt_id
    OR NEW.ordinal IS DISTINCT FROM OLD.ordinal OR NEW.provider_id IS DISTINCT FROM OLD.provider_id
    OR NEW.model_id IS DISTINCT FROM OLD.model_id OR NEW.model_version IS DISTINCT FROM OLD.model_version
    OR NEW.fallback_from_id IS DISTINCT FROM OLD.fallback_from_id OR NEW.started_at IS DISTINCT FROM OLD.started_at
    OR NEW.request_hash IS DISTINCT FROM OLD.request_hash OR NEW.context_manifest_hash IS DISTINCT FROM OLD.context_manifest_hash
    OR NEW.pricing_version IS DISTINCT FROM OLD.pricing_version OR NEW.byok IS DISTINCT FROM OLD.byok
    OR NEW.byok_credential_id IS DISTINCT FROM OLD.byok_credential_id
    OR NEW.byok_credential_version IS DISTINCT FROM OLD.byok_credential_version
    OR NEW.bound_host IS DISTINCT FROM OLD.bound_host OR NEW.secret_version IS DISTINCT FROM OLD.secret_version
    OR NEW.prepare_token_expires_at IS DISTINCT FROM OLD.prepare_token_expires_at
    OR NEW.prepared_event_id IS DISTINCT FROM OLD.prepared_event_id THEN
    RAISE EXCEPTION 'immutable provider attempt scope mutation is forbidden';
  END IF;
  IF NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at THEN
    RAISE EXCEPTION 'provider attempt version must advance exactly once';
  END IF;
  IF OLD.status='prepared' AND OLD.version=1 AND NEW.status IN ('dispatching','abandoned') AND NEW.version=2 THEN RETURN NEW; END IF;
  IF OLD.status='dispatching' AND OLD.version=2 AND NEW.status IN ('completed','failed','cancelled','outcome_unknown') AND NEW.version=3 THEN RETURN NEW; END IF;
  IF OLD.status='outcome_unknown' AND OLD.version=3 AND OLD.usage_status='unknown'
    AND NEW.status='outcome_unknown' AND NEW.version=4 AND NEW.usage_status='confirmed' THEN RETURN NEW; END IF;
  RAISE EXCEPTION 'invalid provider attempt transition %/% -> %/%',OLD.status,OLD.version,NEW.status,NEW.version;
END
$$;

CREATE TRIGGER llm_provider_attempts_lifecycle BEFORE UPDATE OR DELETE ON agent.llm_provider_attempts
  FOR EACH ROW EXECUTE FUNCTION agent.enforce_llm_provider_attempt_lifecycle();

CREATE INDEX llm_provider_attempts_recovery_idx
  ON agent.llm_provider_attempts(tenant_id,status,prepare_token_expires_at,completion_deadline,reconciliation_due_at,id)
  WHERE status IN ('prepared','dispatching','outcome_unknown');

CREATE FUNCTION agent.list_recoverable_llm_provider_tenants(
  p_store_epoch uuid,
  p_after uuid,
  p_limit integer,
  p_shard_index integer,
  p_shard_count integer,
  p_now timestamptz
) RETURNS TABLE(tenant_id text)
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,agent SET row_security=off AS $$
BEGIN
  IF p_store_epoch IS NULL OR p_limit IS NULL OR p_limit<1 OR p_limit>5000
    OR p_shard_count IS NULL OR p_shard_count<1 OR p_shard_index IS NULL
    OR p_shard_index<0 OR p_shard_index>=p_shard_count OR p_now IS NULL THEN
    RAISE EXCEPTION 'invalid LLM provider recovery tenant scan arguments' USING ERRCODE='22023';
  END IF;
  RETURN QUERY
    SELECT p.tenant_id::text
    FROM agent.llm_provider_attempts p
    JOIN agent.events e ON e.tenant_id=p.tenant_id AND e.id=p.prepared_event_id
      AND e.aggregate_kind='provider_attempt' AND e.aggregate_id=p.id
      AND e.aggregate_version=1 AND e.event_type='ProviderAttemptPrepared' AND e.event_schema_version=1
    WHERE e.store_epoch=p_store_epoch
      AND ((p.status='prepared' AND p.prepare_token_expires_at<=p_now)
        OR (p.status='dispatching' AND p.completion_deadline<=p_now)
        OR (p.status='outcome_unknown' AND p.version=3 AND p.reconciliation_due_at<=p_now))
      AND (p_after IS NULL OR p.tenant_id>p_after)
      AND ((hashtextextended(p.tenant_id::text,0) & 9223372036854775807) % p_shard_count)=p_shard_index
    GROUP BY p.tenant_id ORDER BY p.tenant_id LIMIT p_limit;
END
$$;

REVOKE ALL ON FUNCTION agent.list_recoverable_llm_provider_tenants(uuid,uuid,integer,integer,integer,timestamptz) FROM PUBLIC;
