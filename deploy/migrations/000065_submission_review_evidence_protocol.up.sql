DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM product.submissions)
    OR EXISTS (SELECT 1 FROM product.reviews) THEN
    RAISE EXCEPTION 'submission review protocol requires pre-GA submission tables to be empty';
  END IF;
END
$$;

ALTER TABLE product.rubric_versions
  ADD CONSTRAINT rubric_versions_tenant_id_id_unique UNIQUE (tenant_id,id);

ALTER TABLE product.submissions
  ADD COLUMN task_version bigint,
  ADD COLUMN submission_kind text,
  ADD COLUMN understanding_hash text,
  ADD COLUMN payload_manifest_hash text,
  ADD COLUMN understanding_manifest_hash text;

ALTER TABLE product.submissions
  ALTER COLUMN task_version SET NOT NULL,
  ALTER COLUMN submission_kind SET NOT NULL,
  ALTER COLUMN understanding_ref SET NOT NULL,
  ALTER COLUMN understanding_hash SET NOT NULL,
  ALTER COLUMN payload_manifest_hash SET NOT NULL,
  ALTER COLUMN understanding_manifest_hash SET NOT NULL,
  ADD CONSTRAINT submissions_content_contract CHECK (
    task_version>0
    AND submission_kind IN ('code','writing','design')
    AND content_hash ~ '^[0-9a-f]{64}$'
    AND understanding_hash ~ '^[0-9a-f]{64}$'
    AND payload_manifest_hash ~ '^[0-9a-f]{64}$'
    AND understanding_manifest_hash ~ '^[0-9a-f]{64}$'
  ),
  ADD CONSTRAINT submissions_task_fk FOREIGN KEY (tenant_id,daily_task_id)
    REFERENCES product.daily_tasks(tenant_id,id) ON DELETE RESTRICT;

CREATE TABLE product.submission_review_generations (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version>0),
  daily_task_id uuid NOT NULL,
  task_version bigint NOT NULL CHECK (task_version>0),
  submission_id uuid NOT NULL,
  submission_revision integer NOT NULL CHECK (submission_revision>0),
  rubric_version_id uuid NOT NULL,
  status text NOT NULL CHECK (status IN ('generating','succeeded','failed','superseded')),
  input_manifest jsonb NOT NULL CHECK (jsonb_typeof(input_manifest)='object'),
  input_manifest_hash text NOT NULL CHECK (input_manifest_hash ~ '^[0-9a-f]{64}$'),
  behavior_profile text NOT NULL CHECK (behavior_profile='evaluator'),
  behavior_environment text NOT NULL CHECK (behavior_environment IN ('staging','production')),
  behavior_channel_id uuid NOT NULL,
  behavior_channel_sequence bigint NOT NULL CHECK (behavior_channel_sequence>0),
  behavior_snapshot_id text NOT NULL,
  behavior_activated_at timestamptz NOT NULL,
  review_run_id uuid NOT NULL,
  review_id uuid,
  evidence_id uuid,
  failure_reason text,
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  completed_at timestamptz,
  CONSTRAINT submission_review_generations_tenant_id_id_unique UNIQUE (tenant_id,id),
  CONSTRAINT submission_review_generations_submission_unique UNIQUE (tenant_id,submission_id,rubric_version_id),
  CONSTRAINT submission_review_generations_run_unique UNIQUE (tenant_id,review_run_id),
  CONSTRAINT submission_review_generations_review_unique UNIQUE (tenant_id,review_id),
  CONSTRAINT submission_review_generations_evidence_unique UNIQUE (tenant_id,evidence_id),
  CONSTRAINT submission_review_generations_task_fk FOREIGN KEY (tenant_id,daily_task_id)
    REFERENCES product.daily_tasks(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT submission_review_generations_submission_fk FOREIGN KEY (tenant_id,submission_id)
    REFERENCES product.submissions(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT submission_review_generations_rubric_fk FOREIGN KEY (tenant_id,rubric_version_id)
    REFERENCES product.rubric_versions(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT submission_review_generations_run_fk FOREIGN KEY (tenant_id,review_run_id)
    REFERENCES agent.runs(tenant_id,id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  CONSTRAINT submission_review_generations_lifecycle_contract CHECK (
    (status='generating' AND review_id IS NULL AND evidence_id IS NULL AND failure_reason IS NULL AND completed_at IS NULL)
    OR (status='succeeded' AND review_id IS NOT NULL AND evidence_id IS NOT NULL AND failure_reason IS NULL AND completed_at IS NOT NULL)
    OR (status IN ('failed','superseded') AND review_id IS NULL AND evidence_id IS NULL AND char_length(failure_reason) BETWEEN 1 AND 128 AND completed_at IS NOT NULL)
  )
);

ALTER TABLE product.submission_review_generations ENABLE ROW LEVEL SECURITY;
ALTER TABLE product.submission_review_generations FORCE ROW LEVEL SECURITY;
CREATE POLICY submission_review_generations_tenant_isolation ON product.submission_review_generations
  USING (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid);

ALTER TABLE product.reviews
  ADD COLUMN generation_id uuid,
  ADD COLUMN daily_task_id uuid,
  ADD COLUMN submission_revision integer,
  ADD COLUMN deterministic_results_hash text,
  ADD COLUMN review_payload_hash text;

ALTER TABLE product.reviews
  ALTER COLUMN generation_id SET NOT NULL,
  ALTER COLUMN daily_task_id SET NOT NULL,
  ALTER COLUMN submission_revision SET NOT NULL,
  ALTER COLUMN deterministic_results_hash SET NOT NULL,
  ALTER COLUMN review_payload_hash SET NOT NULL,
  ALTER COLUMN evidence_id SET NOT NULL,
  ADD CONSTRAINT reviews_generation_unique UNIQUE (tenant_id,generation_id),
  ADD CONSTRAINT reviews_submission_rubric_unique UNIQUE (tenant_id,submission_id,rubric_version_id),
  ADD CONSTRAINT reviews_content_contract CHECK (
    status='completed'
    AND submission_revision>0
    AND jsonb_typeof(deterministic_results)='object'
    AND deterministic_results_hash ~ '^[0-9a-f]{64}$'
    AND review_payload_hash ~ '^[0-9a-f]{64}$'
  ),
  ADD CONSTRAINT reviews_generation_fk FOREIGN KEY (tenant_id,generation_id)
    REFERENCES product.submission_review_generations(tenant_id,id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  ADD CONSTRAINT reviews_task_fk FOREIGN KEY (tenant_id,daily_task_id)
    REFERENCES product.daily_tasks(tenant_id,id) ON DELETE RESTRICT,
  ADD CONSTRAINT reviews_submission_fk FOREIGN KEY (tenant_id,submission_id)
    REFERENCES product.submissions(tenant_id,id) ON DELETE RESTRICT,
  ADD CONSTRAINT reviews_rubric_fk FOREIGN KEY (tenant_id,rubric_version_id)
    REFERENCES product.rubric_versions(tenant_id,id) ON DELETE RESTRICT,
  ADD CONSTRAINT reviews_evidence_fk FOREIGN KEY (tenant_id,evidence_id)
    REFERENCES product.evidence(tenant_id,id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE product.submission_review_generations
  ADD CONSTRAINT submission_review_generations_review_fk FOREIGN KEY (tenant_id,review_id)
    REFERENCES product.reviews(tenant_id,id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  ADD CONSTRAINT submission_review_generations_evidence_fk FOREIGN KEY (tenant_id,evidence_id)
    REFERENCES product.evidence(tenant_id,id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;

CREATE FUNCTION agent.lock_owned_submission_review(
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
  FOR UPDATE OF d,m;
  RETURN COALESCE(admissible,false);
END
$$;

CREATE FUNCTION agent.list_submission_review_reconciliation_tenants(
  p_after_tenant uuid,
  p_limit integer
) RETURNS TABLE(tenant_id uuid)
LANGUAGE sql
SECURITY DEFINER
SET search_path = pg_catalog, agent, product
SET row_security = off
AS $$
  SELECT DISTINCT g.tenant_id
  FROM product.submission_review_generations g
  JOIN agent.runs r ON r.tenant_id=g.tenant_id AND r.id=g.review_run_id
  WHERE g.status='generating' AND r.status IN ('succeeded','failed','cancelled','expired')
    AND (p_after_tenant IS NULL OR g.tenant_id>p_after_tenant)
  ORDER BY g.tenant_id
  LIMIT LEAST(GREATEST(p_limit,1),5000)
$$;

REVOKE ALL ON FUNCTION agent.lock_owned_submission_review(uuid,uuid,uuid,integer,uuid,bigint) FROM PUBLIC;
REVOKE ALL ON FUNCTION agent.list_submission_review_reconciliation_tenants(uuid,integer) FROM PUBLIC;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service') THEN
    GRANT SELECT,INSERT ON product.submissions,product.reviews,product.evidence,product.submission_review_generations TO lites_product_service;
    GRANT UPDATE ON product.daily_tasks,product.submission_review_generations TO lites_product_service;
    GRANT SELECT ON product.rubric_versions TO lites_product_service;
    GRANT EXECUTE ON FUNCTION agent.lock_owned_submission_review(uuid,uuid,uuid,integer,uuid,bigint) TO lites_product_service;
    GRANT EXECUTE ON FUNCTION agent.list_submission_review_reconciliation_tenants(uuid,integer) TO lites_product_service;
  END IF;
END
$$;
