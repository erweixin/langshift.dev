DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid='product.data_export_requests'::regclass
      AND conname='data_export_requests_scope_contract' AND convalidated
  ) OR NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid='product.data_export_requests'::regclass
      AND conname='data_export_requests_lifecycle_contract' AND convalidated
  ) OR NOT EXISTS (
    SELECT 1 FROM pg_indexes
    WHERE schemaname='product' AND indexname='data_export_requests_owner_created_idx'
  ) OR NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='product' AND table_name='data_export_requests' AND column_name='failure_code'
  ) OR EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_erasure_worker') AND (
    NOT has_table_privilege('lites_erasure_worker','product.data_export_requests','SELECT,UPDATE')
    OR NOT has_table_privilege('lites_erasure_worker','identity.users','SELECT')
    OR NOT has_table_privilege('lites_erasure_worker','identity.memberships','SELECT')
    OR NOT has_table_privilege('lites_erasure_worker','product.capability_claims','SELECT')
    OR NOT has_table_privilege('lites_erasure_worker','product.claim_evidence_links','SELECT')
    OR NOT has_table_privilege('lites_erasure_worker','product.mission_focuses','SELECT')
    OR NOT has_table_privilege('lites_erasure_worker','product.artifacts','SELECT')
    OR NOT has_table_privilege('lites_erasure_worker','agent.conversations','SELECT')
    OR NOT has_table_privilege('lites_erasure_worker','agent.memory_document_revisions','SELECT')
  ) THEN
    RAISE EXCEPTION 'Account export delivery boundary is incomplete';
  END IF;
END
$$;
