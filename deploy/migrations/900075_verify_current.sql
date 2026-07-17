DO $$
DECLARE actual integer;
BEGIN
  SELECT max(version) INTO actual FROM public.lites_schema_migrations;
  IF actual<>75 OR NOT EXISTS (SELECT 1 FROM public.lites_schema_migrations WHERE version=75 AND name='enterprise_privacy_protocol') THEN
    RAISE EXCEPTION 'expected migration version 75';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='share_grants_scope_contract')
    OR NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='share_grants_lifecycle' AND NOT tgisinternal)
    OR NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='aggregate_metrics_privacy_contract')
    OR NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='aggregate_snapshots_tenant_id_id_unique')
    OR NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='aggregate_query_budgets_contract')
    OR to_regclass('product.aggregate_query_audit') IS NULL
    OR NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='aggregate_query_audit_append_only' AND NOT tgisinternal)
  THEN RAISE EXCEPTION 'enterprise privacy protocol is incomplete'; END IF;
END
$$;

SELECT json_build_object('status','passed','schema_version',75,'share_grants','revision_scoped_revocable','aggregate_privacy','stable_suppression_k5_budget100') AS current_schema_verification;
