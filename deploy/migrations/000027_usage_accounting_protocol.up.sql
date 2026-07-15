DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM agent.llm_provider_attempts)
    OR EXISTS (SELECT 1 FROM contracts.usage_reservations)
    OR EXISTS (SELECT 1 FROM contracts.usage_ledger)
    OR EXISTS (SELECT 1 FROM contracts.provider_costs) THEN
    RAISE EXCEPTION 'usage accounting protocol backfill required before migration 27' USING ERRCODE='55000';
  END IF;
END
$$;

ALTER TABLE contracts.credit_buckets
  ADD CONSTRAINT credit_buckets_tenant_id_id_unique UNIQUE (tenant_id,id),
  ADD CONSTRAINT credit_buckets_scope_contract CHECK (
    NULLIF(bucket_kind,'') IS NOT NULL AND granted_units>0 AND starts_at<expires_at
  );

CREATE FUNCTION contracts.enforce_credit_bucket_accounting() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE reserved_delta bigint; settled_delta bigint;
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'credit bucket deletion is forbidden'; END IF;
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.contract_id IS DISTINCT FROM OLD.contract_id OR NEW.bucket_kind IS DISTINCT FROM OLD.bucket_kind
    OR NEW.granted_units IS DISTINCT FROM OLD.granted_units OR NEW.starts_at IS DISTINCT FROM OLD.starts_at
    OR NEW.expires_at IS DISTINCT FROM OLD.expires_at OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'immutable credit bucket scope mutation is forbidden';
  END IF;
  IF NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at THEN
    RAISE EXCEPTION 'credit bucket version must advance exactly once';
  END IF;
  reserved_delta:=NEW.reserved_units-OLD.reserved_units;
  settled_delta:=NEW.settled_units-OLD.settled_units;
  IF reserved_delta>0 AND settled_delta=0 THEN RETURN NEW; END IF;
  IF reserved_delta<0 AND settled_delta=0 THEN RETURN NEW; END IF;
  IF reserved_delta<0 AND settled_delta>=0 AND settled_delta<=-reserved_delta THEN RETURN NEW; END IF;
  RAISE EXCEPTION 'invalid credit bucket accounting transition';
END
$$;

CREATE TRIGGER credit_buckets_accounting BEFORE UPDATE OR DELETE ON contracts.credit_buckets
  FOR EACH ROW EXECUTE FUNCTION contracts.enforce_credit_bucket_accounting();

CREATE FUNCTION contracts.reserve_credit_units(
  p_tenant_id uuid,p_bucket_id uuid,p_units bigint,p_now timestamptz,p_reservation_expires_at timestamptz
) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,contracts SET row_security=off AS $$
DECLARE next_version bigint;
BEGIN
  IF p_tenant_id IS NULL OR p_bucket_id IS NULL OR p_units IS NULL OR p_units<=0
    OR p_now IS NULL OR p_reservation_expires_at IS NULL OR p_reservation_expires_at<=p_now THEN
    RAISE EXCEPTION 'invalid credit reservation arguments' USING ERRCODE='22023';
  END IF;
  IF NULLIF(current_setting('lites.tenant_id',true),'')::uuid IS DISTINCT FROM p_tenant_id THEN
    RAISE EXCEPTION 'credit reservation tenant context mismatch' USING ERRCODE='42501';
  END IF;
  UPDATE contracts.credit_buckets SET version=version+1,reserved_units=reserved_units+p_units,updated_at=p_now
  WHERE tenant_id=p_tenant_id AND id=p_bucket_id AND starts_at<=p_now AND expires_at>=p_reservation_expires_at
    AND granted_units-reserved_units-settled_units>=p_units
  RETURNING version INTO next_version;
  RETURN COALESCE(next_version,0);
END
$$;

CREATE FUNCTION contracts.settle_credit_units(
  p_tenant_id uuid,p_bucket_id uuid,p_reserved_units bigint,p_actual_units bigint,p_now timestamptz
) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,contracts SET row_security=off AS $$
DECLARE next_version bigint;
BEGIN
  IF p_tenant_id IS NULL OR p_bucket_id IS NULL OR p_reserved_units IS NULL OR p_reserved_units<=0
    OR p_actual_units IS NULL OR p_actual_units<0 OR p_actual_units>p_reserved_units OR p_now IS NULL THEN
    RAISE EXCEPTION 'invalid credit settlement arguments' USING ERRCODE='22023';
  END IF;
  IF NULLIF(current_setting('lites.tenant_id',true),'')::uuid IS DISTINCT FROM p_tenant_id THEN
    RAISE EXCEPTION 'credit settlement tenant context mismatch' USING ERRCODE='42501';
  END IF;
  UPDATE contracts.credit_buckets SET version=version+1,reserved_units=reserved_units-p_reserved_units,
    settled_units=settled_units+p_actual_units,updated_at=p_now
  WHERE tenant_id=p_tenant_id AND id=p_bucket_id AND reserved_units>=p_reserved_units
  RETURNING version INTO next_version;
  RETURN COALESCE(next_version,0);
END
$$;

CREATE FUNCTION contracts.release_credit_units(
  p_tenant_id uuid,p_bucket_id uuid,p_reserved_units bigint,p_now timestamptz
) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,contracts SET row_security=off AS $$
DECLARE next_version bigint;
BEGIN
  IF p_tenant_id IS NULL OR p_bucket_id IS NULL OR p_reserved_units IS NULL OR p_reserved_units<=0 OR p_now IS NULL THEN
    RAISE EXCEPTION 'invalid credit release arguments' USING ERRCODE='22023';
  END IF;
  IF NULLIF(current_setting('lites.tenant_id',true),'')::uuid IS DISTINCT FROM p_tenant_id THEN
    RAISE EXCEPTION 'credit release tenant context mismatch' USING ERRCODE='42501';
  END IF;
  UPDATE contracts.credit_buckets SET version=version+1,reserved_units=reserved_units-p_reserved_units,updated_at=p_now
  WHERE tenant_id=p_tenant_id AND id=p_bucket_id AND reserved_units>=p_reserved_units
  RETURNING version INTO next_version;
  RETURN COALESCE(next_version,0);
END
$$;

REVOKE ALL ON FUNCTION contracts.reserve_credit_units(uuid,uuid,bigint,timestamptz,timestamptz) FROM PUBLIC;
REVOKE ALL ON FUNCTION contracts.settle_credit_units(uuid,uuid,bigint,bigint,timestamptz) FROM PUBLIC;
REVOKE ALL ON FUNCTION contracts.release_credit_units(uuid,uuid,bigint,timestamptz) FROM PUBLIC;

ALTER TABLE contracts.usage_reservations
  ADD COLUMN request_id text,
  ADD COLUMN subject_kind text,
  ADD COLUMN subject_id uuid,
  ADD COLUMN subject_version bigint,
  ADD COLUMN reserved_event_id uuid,
  ADD COLUMN terminal_event_id uuid,
  ADD COLUMN ledger_entry_id uuid,
  ADD COLUMN actual_units bigint,
  ADD COLUMN released_units bigint,
  ADD COLUMN provider_attempt_id uuid,
  ADD COLUMN reason_code text;

ALTER TABLE contracts.usage_reservations
  ALTER COLUMN request_id SET NOT NULL,
  ALTER COLUMN subject_kind SET NOT NULL,
  ALTER COLUMN subject_id SET NOT NULL,
  ALTER COLUMN subject_version SET NOT NULL,
  ALTER COLUMN reserved_event_id SET NOT NULL,
  ADD CONSTRAINT usage_reservations_tenant_id_id_unique UNIQUE (tenant_id,id),
  ADD CONSTRAINT usage_reservations_request_unique UNIQUE (tenant_id,user_id,request_id),
  ADD CONSTRAINT usage_reservations_provider_attempt_unique UNIQUE (tenant_id,provider_attempt_id),
  ADD CONSTRAINT usage_reservations_status_contract CHECK (status IN ('reserved','settled','released')),
  ADD CONSTRAINT usage_reservations_scope_contract CHECK (
    NULLIF(request_id,'') IS NOT NULL AND subject_kind IN ('provider_attempt','tool_call')
    AND subject_version>0 AND NULLIF(operation_key,'') IS NOT NULL AND reserved_units>0
  ),
  ADD CONSTRAINT usage_reservations_lifecycle_contract CHECK (
    (status='reserved' AND version=1 AND settled_at IS NULL AND released_at IS NULL
      AND terminal_event_id IS NULL AND ledger_entry_id IS NULL AND actual_units IS NULL
      AND released_units IS NULL AND provider_attempt_id IS NULL AND reason_code IS NULL)
    OR (status='settled' AND version=2 AND settled_at IS NOT NULL AND released_at IS NULL
      AND terminal_event_id IS NOT NULL AND ledger_entry_id IS NOT NULL AND actual_units>=0
      AND actual_units<=reserved_units AND released_units=reserved_units-actual_units
      AND (subject_kind<>'provider_attempt' OR provider_attempt_id=subject_id) AND reason_code IS NULL)
    OR (status='released' AND version=2 AND settled_at IS NULL AND released_at IS NOT NULL
      AND terminal_event_id IS NOT NULL AND ledger_entry_id IS NOT NULL AND actual_units=0
      AND released_units=reserved_units AND reason_code IS NOT NULL)
  ),
  ADD CONSTRAINT usage_reservations_tenant_bucket_fk FOREIGN KEY (tenant_id,bucket_id)
    REFERENCES contracts.credit_buckets(tenant_id,id) ON DELETE RESTRICT,
  ADD CONSTRAINT usage_reservations_reserved_event_fk FOREIGN KEY (reserved_event_id)
    REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  ADD CONSTRAINT usage_reservations_terminal_event_fk FOREIGN KEY (terminal_event_id)
    REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;

CREATE FUNCTION contracts.enforce_usage_reservation_lifecycle() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'usage reservation deletion is forbidden'; END IF;
  IF OLD.status<>'reserved' OR OLD.version<>1 THEN RAISE EXCEPTION 'terminal usage reservation mutation is forbidden'; END IF;
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.user_id IS DISTINCT FROM OLD.user_id OR NEW.created_at IS DISTINCT FROM OLD.created_at
    OR NEW.bucket_id IS DISTINCT FROM OLD.bucket_id OR NEW.operation_key IS DISTINCT FROM OLD.operation_key
    OR NEW.reserved_units IS DISTINCT FROM OLD.reserved_units OR NEW.expires_at IS DISTINCT FROM OLD.expires_at
    OR NEW.request_id IS DISTINCT FROM OLD.request_id OR NEW.subject_kind IS DISTINCT FROM OLD.subject_kind
    OR NEW.subject_id IS DISTINCT FROM OLD.subject_id OR NEW.subject_version IS DISTINCT FROM OLD.subject_version
    OR NEW.reserved_event_id IS DISTINCT FROM OLD.reserved_event_id THEN
    RAISE EXCEPTION 'immutable usage reservation scope mutation is forbidden';
  END IF;
  IF NEW.version<>2 OR NEW.updated_at<OLD.updated_at OR NEW.status NOT IN ('settled','released') THEN
    RAISE EXCEPTION 'invalid usage reservation terminal transition';
  END IF;
  RETURN NEW;
END
$$;

CREATE TRIGGER usage_reservations_lifecycle BEFORE UPDATE OR DELETE ON contracts.usage_reservations
  FOR EACH ROW EXECUTE FUNCTION contracts.enforce_usage_reservation_lifecycle();

ALTER TABLE contracts.usage_ledger
  ADD COLUMN bucket_id uuid,
  ADD COLUMN reservation_version bigint,
  ADD COLUMN event_id uuid,
  ALTER COLUMN reservation_id SET NOT NULL,
  ALTER COLUMN bucket_id SET NOT NULL,
  ALTER COLUMN reservation_version SET NOT NULL,
  ALTER COLUMN event_id SET NOT NULL,
  ADD CONSTRAINT usage_ledger_entry_kind_contract CHECK (entry_kind IN ('settlement','release','adjustment')),
  ADD CONSTRAINT usage_ledger_units_contract CHECK (units>=0),
  ADD CONSTRAINT usage_ledger_reservation_exact_fk FOREIGN KEY (tenant_id,reservation_id)
    REFERENCES contracts.usage_reservations(tenant_id,id) ON DELETE RESTRICT,
  ADD CONSTRAINT usage_ledger_bucket_exact_fk FOREIGN KEY (tenant_id,bucket_id)
    REFERENCES contracts.credit_buckets(tenant_id,id) ON DELETE RESTRICT,
  ADD CONSTRAINT usage_ledger_event_fk FOREIGN KEY (event_id)
    REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE agent.llm_provider_attempts
  ADD COLUMN usage_reservation_id uuid;

ALTER TABLE agent.llm_provider_attempts
  ALTER COLUMN usage_reservation_id SET NOT NULL,
  ADD CONSTRAINT llm_provider_attempts_usage_reservation_unique UNIQUE (tenant_id,usage_reservation_id),
  ADD CONSTRAINT llm_provider_attempts_usage_reservation_fk FOREIGN KEY (tenant_id,usage_reservation_id)
    REFERENCES contracts.usage_reservations(tenant_id,id) ON DELETE RESTRICT;

CREATE FUNCTION agent.enforce_provider_usage_reservation_binding() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.usage_reservation_id IS DISTINCT FROM OLD.usage_reservation_id THEN
    RAISE EXCEPTION 'immutable provider usage reservation mutation is forbidden';
  END IF;
  RETURN NEW;
END
$$;

CREATE TRIGGER llm_provider_attempts_usage_binding BEFORE UPDATE ON agent.llm_provider_attempts
  FOR EACH ROW EXECUTE FUNCTION agent.enforce_provider_usage_reservation_binding();

ALTER TABLE contracts.usage_reservations
  ADD CONSTRAINT usage_reservations_provider_attempt_fk FOREIGN KEY (tenant_id,provider_attempt_id)
    REFERENCES agent.llm_provider_attempts(tenant_id,id) ON DELETE RESTRICT;

ALTER TABLE contracts.provider_costs
  ADD COLUMN provider_attempt_row_id uuid,
  ADD COLUMN model_version text,
  ADD COLUMN pricing_version text,
  ADD COLUMN request_hash text,
  ADD COLUMN response_hash text,
  ADD COLUMN usage_status text,
  ADD COLUMN reservation_id uuid,
  ADD COLUMN recorded_event_id uuid;

ALTER TABLE contracts.provider_costs
  ALTER COLUMN provider_attempt_row_id SET NOT NULL,
  ALTER COLUMN model_version SET NOT NULL,
  ALTER COLUMN pricing_version SET NOT NULL,
  ALTER COLUMN request_hash SET NOT NULL,
  ALTER COLUMN usage_status SET NOT NULL,
  ALTER COLUMN reservation_id SET NOT NULL,
  ALTER COLUMN recorded_event_id SET NOT NULL,
  ADD CONSTRAINT provider_costs_scope_contract CHECK (
    NULLIF(provider_attempt_id,'') IS NOT NULL AND NULLIF(provider_id,'') IS NOT NULL
    AND NULLIF(model_id,'') IS NOT NULL AND NULLIF(model_version,'') IS NOT NULL
    AND NULLIF(pricing_version,'') IS NOT NULL AND NULLIF(request_hash,'') IS NOT NULL
    AND input_tokens>=0 AND output_tokens>=0 AND cost_microunits>=0
    AND usage_status IN ('confirmed','estimated') AND (NOT byok OR cost_microunits=0)
  ),
  ADD CONSTRAINT provider_costs_attempt_fk FOREIGN KEY (tenant_id,provider_attempt_row_id)
    REFERENCES agent.llm_provider_attempts(tenant_id,id) ON DELETE RESTRICT,
  ADD CONSTRAINT provider_costs_reservation_fk FOREIGN KEY (tenant_id,reservation_id)
    REFERENCES contracts.usage_reservations(tenant_id,id) ON DELETE RESTRICT,
  ADD CONSTRAINT provider_costs_recorded_event_fk FOREIGN KEY (recorded_event_id)
    REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;

CREATE INDEX usage_reservations_expiry_idx ON contracts.usage_reservations(tenant_id,expires_at,id)
  WHERE status='reserved';

CREATE FUNCTION contracts.list_releasable_usage_tenants(
  p_store_epoch uuid,p_after uuid,p_limit integer,p_shard_index integer,p_shard_count integer,p_now timestamptz
) RETURNS TABLE(tenant_id text)
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,contracts,agent SET row_security=off AS $$
BEGIN
  IF p_store_epoch IS NULL OR p_limit IS NULL OR p_limit<1 OR p_limit>5000
    OR p_shard_count IS NULL OR p_shard_count<1 OR p_shard_index IS NULL
    OR p_shard_index<0 OR p_shard_index>=p_shard_count OR p_now IS NULL THEN
    RAISE EXCEPTION 'invalid usage release tenant scan arguments' USING ERRCODE='22023';
  END IF;
  RETURN QUERY
    SELECT r.tenant_id::text
    FROM contracts.usage_reservations r
    JOIN agent.events e ON e.tenant_id=r.tenant_id AND e.id=r.reserved_event_id
      AND e.aggregate_kind='usage_reservation' AND e.aggregate_id=r.id
      AND e.aggregate_version=1 AND e.event_type='UsageReserved' AND e.event_schema_version=1
    LEFT JOIN agent.llm_provider_attempts p ON p.tenant_id=r.tenant_id AND p.usage_reservation_id=r.id
    WHERE e.store_epoch=p_store_epoch AND r.status='reserved' AND r.expires_at<=p_now
      AND (p.id IS NULL OR p.status='abandoned')
      AND (p_after IS NULL OR r.tenant_id>p_after)
      AND ((hashtextextended(r.tenant_id::text,0) & 9223372036854775807) % p_shard_count)=p_shard_index
    GROUP BY r.tenant_id ORDER BY r.tenant_id LIMIT p_limit;
END
$$;

REVOKE ALL ON FUNCTION contracts.list_releasable_usage_tenants(uuid,uuid,integer,integer,integer,timestamptz) FROM PUBLIC;
