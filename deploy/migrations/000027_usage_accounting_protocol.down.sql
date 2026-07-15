DROP FUNCTION IF EXISTS contracts.list_releasable_usage_tenants(uuid,uuid,integer,integer,integer,timestamptz);
DROP INDEX IF EXISTS contracts.usage_reservations_expiry_idx;

ALTER TABLE contracts.provider_costs
  DROP CONSTRAINT IF EXISTS provider_costs_recorded_event_fk,
  DROP CONSTRAINT IF EXISTS provider_costs_reservation_fk,
  DROP CONSTRAINT IF EXISTS provider_costs_attempt_fk,
  DROP CONSTRAINT IF EXISTS provider_costs_scope_contract,
  DROP COLUMN IF EXISTS recorded_event_id,
  DROP COLUMN IF EXISTS reservation_id,
  DROP COLUMN IF EXISTS usage_status,
  DROP COLUMN IF EXISTS response_hash,
  DROP COLUMN IF EXISTS request_hash,
  DROP COLUMN IF EXISTS pricing_version,
  DROP COLUMN IF EXISTS model_version,
  DROP COLUMN IF EXISTS provider_attempt_row_id;

ALTER TABLE contracts.usage_reservations DROP CONSTRAINT IF EXISTS usage_reservations_provider_attempt_fk;
DROP TRIGGER IF EXISTS llm_provider_attempts_usage_binding ON agent.llm_provider_attempts;
DROP FUNCTION IF EXISTS agent.enforce_provider_usage_reservation_binding();
ALTER TABLE agent.llm_provider_attempts
  DROP CONSTRAINT IF EXISTS llm_provider_attempts_usage_reservation_fk,
  DROP CONSTRAINT IF EXISTS llm_provider_attempts_usage_reservation_unique,
  DROP COLUMN IF EXISTS usage_reservation_id;

ALTER TABLE contracts.usage_ledger
  DROP CONSTRAINT IF EXISTS usage_ledger_event_fk,
  DROP CONSTRAINT IF EXISTS usage_ledger_bucket_exact_fk,
  DROP CONSTRAINT IF EXISTS usage_ledger_reservation_exact_fk,
  DROP CONSTRAINT IF EXISTS usage_ledger_units_contract,
  DROP CONSTRAINT IF EXISTS usage_ledger_entry_kind_contract,
  DROP COLUMN IF EXISTS event_id,
  DROP COLUMN IF EXISTS reservation_version,
  DROP COLUMN IF EXISTS bucket_id;
ALTER TABLE contracts.usage_ledger ALTER COLUMN reservation_id DROP NOT NULL;

DROP TRIGGER IF EXISTS usage_reservations_lifecycle ON contracts.usage_reservations;
DROP FUNCTION IF EXISTS contracts.enforce_usage_reservation_lifecycle();
ALTER TABLE contracts.usage_reservations
  DROP CONSTRAINT IF EXISTS usage_reservations_terminal_event_fk,
  DROP CONSTRAINT IF EXISTS usage_reservations_reserved_event_fk,
  DROP CONSTRAINT IF EXISTS usage_reservations_tenant_bucket_fk,
  DROP CONSTRAINT IF EXISTS usage_reservations_lifecycle_contract,
  DROP CONSTRAINT IF EXISTS usage_reservations_scope_contract,
  DROP CONSTRAINT IF EXISTS usage_reservations_status_contract,
  DROP CONSTRAINT IF EXISTS usage_reservations_provider_attempt_unique,
  DROP CONSTRAINT IF EXISTS usage_reservations_request_unique,
  DROP CONSTRAINT IF EXISTS usage_reservations_tenant_id_id_unique,
  DROP COLUMN IF EXISTS reason_code,
  DROP COLUMN IF EXISTS provider_attempt_id,
  DROP COLUMN IF EXISTS released_units,
  DROP COLUMN IF EXISTS actual_units,
  DROP COLUMN IF EXISTS ledger_entry_id,
  DROP COLUMN IF EXISTS terminal_event_id,
  DROP COLUMN IF EXISTS reserved_event_id,
  DROP COLUMN IF EXISTS subject_version,
  DROP COLUMN IF EXISTS subject_id,
  DROP COLUMN IF EXISTS subject_kind,
  DROP COLUMN IF EXISTS request_id;

DROP TRIGGER IF EXISTS credit_buckets_accounting ON contracts.credit_buckets;
DROP FUNCTION IF EXISTS contracts.release_credit_units(uuid,uuid,bigint,timestamptz);
DROP FUNCTION IF EXISTS contracts.settle_credit_units(uuid,uuid,bigint,bigint,timestamptz);
DROP FUNCTION IF EXISTS contracts.reserve_credit_units(uuid,uuid,bigint,timestamptz,timestamptz);
DROP FUNCTION IF EXISTS contracts.enforce_credit_bucket_accounting();
ALTER TABLE contracts.credit_buckets
  DROP CONSTRAINT IF EXISTS credit_buckets_scope_contract,
  DROP CONSTRAINT IF EXISTS credit_buckets_tenant_id_id_unique;
