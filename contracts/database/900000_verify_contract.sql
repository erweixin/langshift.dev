\set ON_ERROR_STOP on

BEGIN;

DO $verify_catalog$
DECLARE
  actual integer;
BEGIN
  SELECT count(*) INTO actual
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
  WHERE c.relkind = 'r' AND n.nspname IN ('identity','product','agent','contracts');
  IF actual <> 90 THEN RAISE EXCEPTION 'expected 90 contract tables, found %', actual; END IF;

  SELECT count(*) INTO actual
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
  WHERE c.relkind = 'r' AND n.nspname IN ('identity','product','agent','contracts')
    AND c.relrowsecurity AND c.relforcerowsecurity;
  IF actual <> 83 THEN RAISE EXCEPTION 'expected 83 forced-RLS tables, found %', actual; END IF;

  SELECT count(*) INTO actual
  FROM pg_trigger t
  JOIN pg_class c ON c.oid = t.tgrelid
  JOIN pg_namespace n ON n.oid = c.relnamespace
  WHERE NOT t.tgisinternal AND t.tgname LIKE '%_append_only'
    AND n.nspname IN ('identity','product','agent','contracts');
  IF actual <> 21 THEN RAISE EXCEPTION 'expected 21 append-only triggers, found %', actual; END IF;

  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='identity.onboarding_claims'::regclass AND contype='u' AND pg_get_constraintdef(oid) LIKE '%claim_key%') THEN RAISE EXCEPTION 'claim_key uniqueness missing'; END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='identity' AND table_name='sessions' AND column_name='active_tenant_id' AND is_nullable='NO') THEN RAISE EXCEPTION 'session active tenant binding missing'; END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='identity' AND table_name='sessions' AND column_name='version') THEN RAISE EXCEPTION 'session CAS version missing'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='identity.sessions'::regclass AND contype='f' AND pg_get_constraintdef(oid) LIKE '%active_tenant_id%identity.tenants%') THEN RAISE EXCEPTION 'session active tenant foreign key missing'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='agent.idempotency_responses'::regclass AND contype='u' AND pg_get_constraintdef(oid) LIKE '%tenant_id%user_id%operation_id%idempotency_key_hash%') THEN RAISE EXCEPTION 'idempotency response scope uniqueness missing'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='product.mission_focuses'::regclass AND contype='p' AND pg_get_constraintdef(oid) LIKE '%tenant_id%user_id%') THEN RAISE EXCEPTION 'Mission Focus primary key missing'; END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='product' AND table_name='route_revisions' AND column_name='claim_set_hash') THEN RAISE EXCEPTION 'Route claim_set_hash CAS input missing'; END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='product' AND table_name='route_revisions' AND column_name='base_route_version') THEN RAISE EXCEPTION 'Route base_route_version CAS input missing'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='product.reminder_deliveries'::regclass AND contype='u' AND pg_get_constraintdef(oid) LIKE '%schedule_id%occurrence_at%') THEN RAISE EXCEPTION 'reminder delivery dedupe missing'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='agent.tool_effects'::regclass AND contype='u' AND pg_get_constraintdef(oid) LIKE '%effect_scope%provider_id%tool_name%effect_key%') THEN RAISE EXCEPTION 'effect ledger uniqueness missing'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='agent.repair_approvals'::regclass AND contype='u' AND pg_get_constraintdef(oid) LIKE '%repair_command_id%approver_user_id%') THEN RAISE EXCEPTION 'two-person distinct-vote constraint missing'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='contracts.credit_buckets'::regclass AND contype='c' AND pg_get_constraintdef(oid) LIKE '%reserved_units%settled_units%granted_units%') THEN RAISE EXCEPTION 'credit conservation check missing'; END IF;
END
$verify_catalog$;

INSERT INTO identity.users (id, normalized_email, status)
VALUES ('10000000-0000-0000-0000-000000000001', 'contract-probe@lites.invalid', 'active');
INSERT INTO identity.tenants (id, kind, name, status, region)
VALUES
  ('20000000-0000-0000-0000-000000000001', 'enterprise', 'Probe A', 'active', 'US'),
  ('20000000-0000-0000-0000-000000000002', 'enterprise', 'Probe B', 'active', 'US');
INSERT INTO identity.memberships (id, tenant_id, user_id, role, status, joined_at)
VALUES ('30000000-0000-0000-0000-000000000001', '20000000-0000-0000-0000-000000000001', '10000000-0000-0000-0000-000000000001', 'member', 'active', CURRENT_TIMESTAMP);

CREATE ROLE lites_contract_probe NOLOGIN;
GRANT USAGE ON SCHEMA identity TO lites_contract_probe;
GRANT SELECT ON identity.memberships TO lites_contract_probe;
SET LOCAL lites.tenant_id = '20000000-0000-0000-0000-000000000001';
SET LOCAL ROLE lites_contract_probe;
DO $verify_rls_visible$
BEGIN
  IF (SELECT count(*) FROM identity.memberships) <> 1 THEN RAISE EXCEPTION 'same-tenant row is not visible'; END IF;
END
$verify_rls_visible$;
RESET ROLE;
SET LOCAL lites.tenant_id = '20000000-0000-0000-0000-000000000002';
SET LOCAL ROLE lites_contract_probe;
DO $verify_rls_hidden$
BEGIN
  IF (SELECT count(*) FROM identity.memberships) <> 0 THEN RAISE EXCEPTION 'cross-tenant row became visible'; END IF;
END
$verify_rls_hidden$;
RESET ROLE;

INSERT INTO identity.security_events (id, tenant_id, event_type, request_id, details, occurred_at)
VALUES ('40000000-0000-0000-0000-000000000001', '20000000-0000-0000-0000-000000000001', 'contract_probe', 'contract-probe-request', '{}'::jsonb, CURRENT_TIMESTAMP);
DO $verify_append_only$
BEGIN
  BEGIN
    UPDATE identity.security_events SET event_type='mutated' WHERE id='40000000-0000-0000-0000-000000000001';
    RAISE EXCEPTION 'append-only mutation unexpectedly succeeded';
  EXCEPTION WHEN SQLSTATE '55000' THEN
    NULL;
  END;
END
$verify_append_only$;

ROLLBACK;

SELECT json_build_object(
  'status','passed',
  'postgres_version',current_setting('server_version'),
  'table_count',90,
  'forced_rls_count',83,
  'append_only_trigger_count',21,
  'cross_tenant_visible_rows',0,
  'append_only_mutations_succeeded',0,
  'critical_constraints_verified',12
) AS contract_verification;
