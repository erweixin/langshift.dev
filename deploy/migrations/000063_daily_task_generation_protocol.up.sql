DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM product.daily_tasks)
    OR EXISTS (SELECT 1 FROM product.submissions)
    OR EXISTS (SELECT 1 FROM product.reviews) THEN
    RAISE EXCEPTION 'daily task generation protocol requires pre-GA task tables to be empty';
  END IF;
END
$$;

ALTER TABLE product.daily_tasks
  ADD COLUMN generation_id uuid,
  ADD COLUMN task_payload_hash text,
  ADD COLUMN causal_manifest_hash text,
  ADD COLUMN focus_version bigint,
  ADD COLUMN difficulty text,
  ADD COLUMN current_submission_id uuid,
  ADD COLUMN current_review_id uuid,
  ADD COLUMN rescheduled_to date;

ALTER TABLE product.daily_tasks
  ALTER COLUMN generation_id SET NOT NULL,
  ALTER COLUMN task_payload_hash SET NOT NULL,
  ALTER COLUMN causal_manifest_hash SET NOT NULL,
  ALTER COLUMN focus_version SET NOT NULL,
  ALTER COLUMN difficulty SET NOT NULL;

ALTER TABLE product.route_revisions
  ADD CONSTRAINT route_revisions_tenant_id_id_unique UNIQUE (tenant_id,id);

ALTER TABLE product.daily_tasks
  ADD CONSTRAINT daily_tasks_generation_unique UNIQUE (tenant_id,generation_id),
  ADD CONSTRAINT daily_tasks_tenant_id_id_unique UNIQUE (tenant_id,id),
  ADD CONSTRAINT daily_tasks_content_contract CHECK (
    task_payload_hash ~ '^[0-9a-f]{64}$'
    AND causal_manifest_hash ~ '^[0-9a-f]{64}$'
    AND focus_version>0
    AND difficulty IN ('easier','standard','harder')
    AND practice_kind IN ('code','writing','design')
    AND estimated_minutes BETWEEN 5 AND 480
    AND jsonb_typeof(causal_manifest)='object'
  ),
  ADD CONSTRAINT daily_tasks_lifecycle_contract CHECK (
    (status IN ('scheduled','in_progress','skipped') AND current_submission_id IS NULL AND current_review_id IS NULL AND completed_at IS NULL AND rescheduled_to IS NULL)
    OR (status='rescheduled' AND current_submission_id IS NULL AND current_review_id IS NULL AND completed_at IS NULL AND rescheduled_to IS NOT NULL)
    OR (status='submitted' AND current_submission_id IS NOT NULL AND current_review_id IS NULL AND completed_at IS NULL AND rescheduled_to IS NULL)
    OR (status='reviewing' AND current_submission_id IS NOT NULL AND current_review_id IS NOT NULL AND completed_at IS NULL AND rescheduled_to IS NULL)
    OR (status='completed' AND current_submission_id IS NOT NULL AND current_review_id IS NOT NULL AND completed_at IS NOT NULL AND rescheduled_to IS NULL)
  );

ALTER TABLE product.submissions
  ADD CONSTRAINT submissions_tenant_id_id_unique UNIQUE (tenant_id,id);

ALTER TABLE product.reviews
  ADD CONSTRAINT reviews_tenant_id_id_unique UNIQUE (tenant_id,id);

CREATE UNIQUE INDEX daily_tasks_one_daily_commitment
  ON product.daily_tasks(tenant_id,user_id,scheduled_for)
  WHERE status<>'rescheduled';

CREATE TABLE product.daily_task_generations (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version>0),
  mission_id uuid NOT NULL,
  route_revision_id uuid NOT NULL,
  focus_version bigint NOT NULL CHECK (focus_version>0),
  scheduled_for date NOT NULL,
  difficulty text NOT NULL CHECK (difficulty IN ('easier','standard','harder')),
  available_minutes integer NOT NULL CHECK (available_minutes BETWEEN 5 AND 480),
  status text NOT NULL CHECK (status IN ('generating','succeeded','failed','superseded')),
  input_manifest jsonb NOT NULL CHECK (jsonb_typeof(input_manifest)='object'),
  input_manifest_hash text NOT NULL CHECK (input_manifest_hash ~ '^[0-9a-f]{64}$'),
  behavior_profile text NOT NULL,
  behavior_environment text NOT NULL CHECK (behavior_environment IN ('staging','production')),
  behavior_channel_id uuid NOT NULL,
  behavior_channel_sequence bigint NOT NULL CHECK (behavior_channel_sequence>0),
  behavior_snapshot_id text NOT NULL,
  behavior_activated_at timestamptz NOT NULL,
  content_snapshot_id text NOT NULL,
  planner_command_id uuid NOT NULL,
  planner_run_id uuid,
  task_id uuid,
  failure_reason text,
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  completed_at timestamptz,
  CONSTRAINT daily_task_generations_tenant_id_fk FOREIGN KEY (tenant_id) REFERENCES identity.tenants(id) ON DELETE CASCADE,
  CONSTRAINT daily_task_generations_mission_fk FOREIGN KEY (tenant_id,mission_id) REFERENCES product.missions(tenant_id,id) ON DELETE CASCADE,
  CONSTRAINT daily_task_generations_route_fk FOREIGN KEY (tenant_id,route_revision_id) REFERENCES product.route_revisions(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT daily_task_generations_run_fk FOREIGN KEY (tenant_id,planner_run_id) REFERENCES agent.runs(tenant_id,id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  CONSTRAINT daily_task_generations_task_fk FOREIGN KEY (tenant_id,task_id) REFERENCES product.daily_tasks(tenant_id,id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  CONSTRAINT daily_task_generations_command_unique UNIQUE (tenant_id,planner_command_id),
  CONSTRAINT daily_task_generations_tenant_id_id_unique UNIQUE (tenant_id,id),
  CONSTRAINT daily_task_generations_run_unique UNIQUE (tenant_id,planner_run_id),
  CONSTRAINT daily_task_generations_task_unique UNIQUE (tenant_id,task_id),
  CONSTRAINT daily_task_generations_lifecycle_contract CHECK (
    (status='generating' AND planner_run_id IS NOT NULL AND task_id IS NULL AND failure_reason IS NULL AND completed_at IS NULL)
    OR (status='succeeded' AND planner_run_id IS NOT NULL AND task_id IS NOT NULL AND failure_reason IS NULL AND completed_at IS NOT NULL)
    OR (status='failed' AND planner_run_id IS NOT NULL AND task_id IS NULL AND char_length(failure_reason) BETWEEN 1 AND 128 AND completed_at IS NOT NULL)
    OR (status='superseded' AND task_id IS NULL AND planner_run_id IS NULL AND failure_reason='focus_or_route_changed' AND completed_at IS NOT NULL)
  )
);

ALTER TABLE product.daily_task_generations ENABLE ROW LEVEL SECURITY;
ALTER TABLE product.daily_task_generations FORCE ROW LEVEL SECURITY;
CREATE POLICY daily_task_generations_tenant_isolation ON product.daily_task_generations
  USING (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid);

ALTER TABLE product.daily_tasks
  ADD CONSTRAINT daily_tasks_generation_fk FOREIGN KEY (tenant_id,generation_id)
    REFERENCES product.daily_task_generations(tenant_id,id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  ADD CONSTRAINT daily_tasks_current_submission_fk FOREIGN KEY (tenant_id,current_submission_id)
    REFERENCES product.submissions(tenant_id,id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  ADD CONSTRAINT daily_tasks_current_review_fk FOREIGN KEY (tenant_id,current_review_id)
    REFERENCES product.reviews(tenant_id,id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;

CREATE FUNCTION agent.lock_owned_daily_planning_mission(
  p_tenant_id uuid,
  p_user_id uuid,
  p_mission_id uuid,
  p_route_revision_id uuid,
  p_focus_version bigint
) RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, product
SET row_security = off
AS $$
BEGIN
  IF p_tenant_id IS NULL OR p_user_id IS NULL OR p_mission_id IS NULL OR p_route_revision_id IS NULL OR p_focus_version IS NULL THEN
    RAISE EXCEPTION 'daily planning ownership scope is required' USING ERRCODE='22023';
  END IF;
  PERFORM 1
  FROM product.missions m
  JOIN product.mission_focuses f ON f.tenant_id=m.tenant_id AND f.user_id=m.user_id AND f.mission_id=m.id
  JOIN product.route_revisions r ON r.tenant_id=m.tenant_id AND r.user_id=m.user_id AND r.mission_id=m.id AND r.id=p_route_revision_id
  WHERE m.tenant_id=p_tenant_id AND m.user_id=p_user_id AND m.id=p_mission_id
    AND m.status='active' AND m.current_route_revision_id=p_route_revision_id
    AND f.focus_version=p_focus_version AND r.status='accepted'
  FOR KEY SHARE OF m, f, r;
  RETURN FOUND;
END
$$;

REVOKE ALL ON FUNCTION agent.lock_owned_daily_planning_mission(uuid,uuid,uuid,uuid,bigint) FROM PUBLIC;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service') THEN
    GRANT SELECT,INSERT,UPDATE ON product.daily_tasks,product.daily_task_generations TO lites_product_service;
    GRANT SELECT,INSERT ON product.submissions,product.reviews TO lites_product_service;
    GRANT SELECT,INSERT,UPDATE ON product.evidence TO lites_product_service;
    GRANT EXECUTE ON FUNCTION agent.lock_owned_daily_planning_mission(uuid,uuid,uuid,uuid,bigint) TO lites_product_service;
  END IF;
END
$$;
