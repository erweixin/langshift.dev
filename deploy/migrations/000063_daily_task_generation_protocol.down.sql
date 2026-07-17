DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service') THEN
    REVOKE EXECUTE ON FUNCTION agent.lock_owned_daily_planning_mission(uuid,uuid,uuid,uuid,bigint) FROM lites_product_service;
    REVOKE SELECT,INSERT,UPDATE ON product.daily_task_generations,product.daily_tasks FROM lites_product_service;
    REVOKE SELECT,INSERT ON product.submissions,product.reviews FROM lites_product_service;
    REVOKE INSERT ON product.evidence FROM lites_product_service;
  END IF;
END
$$;

DROP FUNCTION IF EXISTS agent.lock_owned_daily_planning_mission(uuid,uuid,uuid,uuid,bigint);

ALTER TABLE product.daily_tasks
  DROP CONSTRAINT IF EXISTS daily_tasks_generation_fk,
  DROP CONSTRAINT IF EXISTS daily_tasks_current_submission_fk,
  DROP CONSTRAINT IF EXISTS daily_tasks_current_review_fk;

DROP POLICY IF EXISTS daily_task_generations_tenant_isolation ON product.daily_task_generations;
DROP TABLE IF EXISTS product.daily_task_generations;

DROP INDEX IF EXISTS product.daily_tasks_one_daily_commitment;

ALTER TABLE product.reviews DROP CONSTRAINT IF EXISTS reviews_tenant_id_id_unique;
ALTER TABLE product.submissions DROP CONSTRAINT IF EXISTS submissions_tenant_id_id_unique;
ALTER TABLE product.route_revisions DROP CONSTRAINT IF EXISTS route_revisions_tenant_id_id_unique;

ALTER TABLE product.daily_tasks
  DROP CONSTRAINT IF EXISTS daily_tasks_generation_unique,
  DROP CONSTRAINT IF EXISTS daily_tasks_tenant_id_id_unique,
  DROP CONSTRAINT IF EXISTS daily_tasks_content_contract,
  DROP CONSTRAINT IF EXISTS daily_tasks_lifecycle_contract,
  DROP COLUMN IF EXISTS generation_id,
  DROP COLUMN IF EXISTS task_payload_hash,
  DROP COLUMN IF EXISTS causal_manifest_hash,
  DROP COLUMN IF EXISTS focus_version,
  DROP COLUMN IF EXISTS difficulty,
  DROP COLUMN IF EXISTS current_submission_id,
  DROP COLUMN IF EXISTS current_review_id,
  DROP COLUMN IF EXISTS rescheduled_to;
