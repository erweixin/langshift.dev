DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service') THEN
    REVOKE SELECT ON identity.memberships FROM lites_product_service;
    REVOKE USAGE ON SCHEMA identity FROM lites_product_service;
    REVOKE SELECT,INSERT,UPDATE ON product.share_grants FROM lites_product_service;
    REVOKE SELECT ON product.aggregate_metrics,product.aggregate_snapshots FROM lites_product_service;
    REVOKE SELECT,INSERT ON product.aggregate_suppressions FROM lites_product_service;
    REVOKE SELECT,INSERT,UPDATE ON product.aggregate_query_budgets FROM lites_product_service;
    REVOKE SELECT,INSERT ON product.aggregate_query_audit FROM lites_product_service;
  END IF;
END
$$;

DROP INDEX product.aggregate_query_audit_sequence_idx;
DROP TRIGGER aggregate_query_audit_append_only ON product.aggregate_query_audit;
DROP POLICY aggregate_query_audit_tenant_isolation ON product.aggregate_query_audit;
DROP TABLE product.aggregate_query_audit;

ALTER TABLE product.aggregate_query_budgets DROP CONSTRAINT aggregate_query_budgets_contract;
ALTER TABLE product.aggregate_query_budgets DROP CONSTRAINT aggregate_query_budgets_tenant_snapshot_fk;
ALTER TABLE product.aggregate_suppressions
  DROP CONSTRAINT aggregate_suppressions_privacy_contract,
  DROP CONSTRAINT aggregate_suppressions_tenant_snapshot_fk;
ALTER TABLE product.aggregate_snapshots
  DROP CONSTRAINT aggregate_snapshots_result_hash_contract,
  DROP CONSTRAINT aggregate_snapshots_tenant_metric_fk,
  DROP CONSTRAINT aggregate_snapshots_tenant_id_id_unique;
ALTER TABLE product.aggregate_metrics
  DROP CONSTRAINT aggregate_metrics_privacy_contract,
  DROP CONSTRAINT aggregate_metrics_tenant_id_id_unique;
DROP TRIGGER share_grants_lifecycle ON product.share_grants;
DROP FUNCTION product.enforce_share_grant_lifecycle();
DROP INDEX product.share_grants_grantee_lookup;
DROP INDEX product.share_grants_exact_scope_unique;
ALTER TABLE product.share_grants
  DROP CONSTRAINT share_grants_scope_contract,
  DROP CONSTRAINT share_grants_grantee_fk,
  DROP CONSTRAINT share_grants_owner_fk;
