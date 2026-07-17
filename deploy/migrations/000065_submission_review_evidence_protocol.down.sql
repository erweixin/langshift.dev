DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service') THEN
    REVOKE EXECUTE ON FUNCTION agent.lock_owned_submission_review(uuid,uuid,uuid,integer,uuid,bigint) FROM lites_product_service;
    REVOKE EXECUTE ON FUNCTION agent.list_submission_review_reconciliation_tenants(uuid,integer) FROM lites_product_service;
    REVOKE SELECT ON product.rubric_versions FROM lites_product_service;
    REVOKE SELECT,INSERT,UPDATE ON product.submission_review_generations FROM lites_product_service;
  END IF;
END
$$;

DROP FUNCTION IF EXISTS agent.list_submission_review_reconciliation_tenants(uuid,integer);
DROP FUNCTION IF EXISTS agent.lock_owned_submission_review(uuid,uuid,uuid,integer,uuid,bigint);

ALTER TABLE product.submission_review_generations
  DROP CONSTRAINT IF EXISTS submission_review_generations_evidence_fk,
  DROP CONSTRAINT IF EXISTS submission_review_generations_review_fk;

ALTER TABLE product.reviews
  DROP CONSTRAINT IF EXISTS reviews_evidence_fk,
  DROP CONSTRAINT IF EXISTS reviews_rubric_fk,
  DROP CONSTRAINT IF EXISTS reviews_submission_fk,
  DROP CONSTRAINT IF EXISTS reviews_task_fk,
  DROP CONSTRAINT IF EXISTS reviews_generation_fk,
  DROP CONSTRAINT IF EXISTS reviews_content_contract,
  DROP CONSTRAINT IF EXISTS reviews_submission_rubric_unique,
  DROP CONSTRAINT IF EXISTS reviews_generation_unique,
  DROP COLUMN IF EXISTS review_payload_hash,
  DROP COLUMN IF EXISTS deterministic_results_hash,
  DROP COLUMN IF EXISTS submission_revision,
  DROP COLUMN IF EXISTS daily_task_id,
  DROP COLUMN IF EXISTS generation_id,
  ALTER COLUMN evidence_id DROP NOT NULL;

DROP TABLE IF EXISTS product.submission_review_generations;

ALTER TABLE product.submissions
  DROP CONSTRAINT IF EXISTS submissions_task_fk,
  DROP CONSTRAINT IF EXISTS submissions_content_contract,
  DROP COLUMN IF EXISTS understanding_manifest_hash,
  DROP COLUMN IF EXISTS payload_manifest_hash,
  DROP COLUMN IF EXISTS understanding_hash,
  DROP COLUMN IF EXISTS submission_kind,
  DROP COLUMN IF EXISTS task_version,
  ALTER COLUMN understanding_ref DROP NOT NULL;

ALTER TABLE product.rubric_versions
  DROP CONSTRAINT IF EXISTS rubric_versions_tenant_id_id_unique;
