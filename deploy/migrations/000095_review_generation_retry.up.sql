BEGIN;

ALTER TABLE product.submission_review_generations
  DROP CONSTRAINT submission_review_generations_submission_unique;

CREATE UNIQUE INDEX submission_review_generations_active_submission_unique
  ON product.submission_review_generations(tenant_id,submission_id,rubric_version_id)
  WHERE status IN ('generating','succeeded');

CREATE OR REPLACE FUNCTION agent.lock_owned_submission_review(
  p_tenant_id uuid,
  p_user_id uuid,
  p_submission_id uuid,
  p_submission_revision integer,
  p_rubric_version_id uuid,
  p_task_version bigint
) RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, agent, product
SET row_security = off
AS $$
DECLARE admissible boolean;
BEGIN
  SELECT true INTO admissible
  FROM product.submissions s
  JOIN product.daily_tasks d ON d.tenant_id=s.tenant_id AND d.id=s.daily_task_id
  JOIN product.missions m ON m.tenant_id=d.tenant_id AND m.id=d.mission_id
  JOIN product.mission_focuses f ON f.tenant_id=m.tenant_id AND f.user_id=m.user_id AND f.mission_id=m.id
  JOIN product.route_revisions r ON r.tenant_id=d.tenant_id AND r.id=d.route_revision_id
  JOIN product.rubric_versions rv ON rv.tenant_id=s.tenant_id AND rv.id=p_rubric_version_id
  WHERE s.tenant_id=p_tenant_id AND s.user_id=p_user_id AND s.id=p_submission_id
    AND s.submission_revision=p_submission_revision
    AND d.user_id=p_user_id AND d.version=p_task_version AND d.status='submitted'
    AND d.current_submission_id=s.id AND d.current_review_id IS NULL
    AND m.user_id=p_user_id AND m.status='active' AND m.current_route_revision_id=d.route_revision_id
    AND r.status='accepted' AND rv.status='active'
    AND NOT EXISTS (
      SELECT 1 FROM product.submission_review_generations g
      WHERE g.tenant_id=p_tenant_id AND g.submission_id=p_submission_id
        AND g.rubric_version_id=p_rubric_version_id
        AND g.status IN ('generating','succeeded')
    )
  FOR UPDATE OF d,m;
  RETURN COALESCE(admissible,false);
END
$$;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service') THEN
    GRANT EXECUTE ON FUNCTION agent.lock_owned_submission_review(uuid,uuid,uuid,integer,uuid,bigint)
      TO lites_product_service;
  END IF;
END
$$;

COMMIT;
