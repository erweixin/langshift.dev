DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM product.programs
    WHERE length(btrim(name)) NOT BETWEEN 1 AND 200 OR status NOT IN ('active','paused','archived')
      OR jsonb_typeof(settings)<>'object'
  ) OR EXISTS (
    SELECT 1 FROM product.cohorts
    WHERE length(btrim(name)) NOT BETWEEN 1 AND 200 OR status NOT IN ('scheduled','active','completed','archived')
      OR starts_at IS NOT NULL AND ends_at IS NOT NULL AND ends_at<=starts_at
  ) OR EXISTS (
    SELECT 1 FROM product.enrollments
    WHERE status NOT IN ('active','left') OR (status='active')<>(left_at IS NULL) OR left_at IS NOT NULL AND left_at<enrolled_at
  ) OR EXISTS (
    SELECT 1 FROM product.role_packs
    WHERE revision<1 OR status<>'published' OR jsonb_typeof(role_profile_ids)<>'array'
      OR jsonb_array_length(role_profile_ids)=0 OR jsonb_typeof(task_template_ids)<>'array'
      OR jsonb_array_length(task_template_ids)=0 OR published_at IS NULL
  ) THEN
    RAISE EXCEPTION 'enterprise program backfill required before migration 76' USING ERRCODE='55000';
  END IF;
END
$$;

ALTER TABLE product.programs
  ADD CONSTRAINT programs_tenant_id_id_unique UNIQUE (tenant_id,id),
  ADD CONSTRAINT programs_owner_membership_fk FOREIGN KEY (tenant_id,owner_user_id)
    REFERENCES identity.memberships(tenant_id,user_id) ON DELETE RESTRICT,
  ADD CONSTRAINT programs_contract CHECK (
    length(btrim(name)) BETWEEN 1 AND 200 AND status IN ('active','paused','archived')
    AND jsonb_typeof(settings)='object'
  );

ALTER TABLE product.cohorts
  ADD CONSTRAINT cohorts_tenant_id_id_unique UNIQUE (tenant_id,id),
  ADD CONSTRAINT cohorts_tenant_program_fk FOREIGN KEY (tenant_id,program_id)
    REFERENCES product.programs(tenant_id,id) ON DELETE RESTRICT,
  ADD CONSTRAINT cohorts_contract CHECK (
    length(btrim(name)) BETWEEN 1 AND 200 AND status IN ('scheduled','active','completed','archived')
    AND (starts_at IS NULL OR ends_at IS NULL OR ends_at>starts_at)
  );

ALTER TABLE product.enrollments
  ADD CONSTRAINT enrollments_tenant_id_id_unique UNIQUE (tenant_id,id),
  ADD CONSTRAINT enrollments_tenant_cohort_fk FOREIGN KEY (tenant_id,cohort_id)
    REFERENCES product.cohorts(tenant_id,id) ON DELETE RESTRICT,
  ADD CONSTRAINT enrollments_member_fk FOREIGN KEY (tenant_id,user_id)
    REFERENCES identity.memberships(tenant_id,user_id) ON DELETE RESTRICT,
  ADD CONSTRAINT enrollments_contract CHECK (
    status IN ('active','left') AND (status='active')=(left_at IS NULL)
    AND (left_at IS NULL OR left_at>=enrolled_at)
  );

ALTER TABLE product.role_packs
  ADD CONSTRAINT role_packs_tenant_id_id_unique UNIQUE (tenant_id,id),
  ADD CONSTRAINT role_packs_tenant_program_fk FOREIGN KEY (tenant_id,program_id)
    REFERENCES product.programs(tenant_id,id) ON DELETE RESTRICT,
  ADD CONSTRAINT role_packs_contract CHECK (
    revision>0 AND status='published'
    AND jsonb_typeof(role_profile_ids)='array' AND jsonb_array_length(role_profile_ids)>0
    AND jsonb_typeof(task_template_ids)='array' AND jsonb_array_length(task_template_ids)>0
    AND published_at IS NOT NULL
  );

CREATE FUNCTION product.enforce_program_lifecycle() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog,product AS $$
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'program deletion is forbidden'; END IF;
  IF OLD.status='archived' THEN RAISE EXCEPTION 'archived program is immutable'; END IF;
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.owner_user_id IS DISTINCT FROM OLD.owner_user_id OR NEW.created_at IS DISTINCT FROM OLD.created_at
    OR NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at
    OR NOT ((OLD.status='active' AND NEW.status IN ('active','paused','archived'))
      OR (OLD.status='paused' AND NEW.status IN ('active','paused','archived')))
  THEN RAISE EXCEPTION 'invalid program transition'; END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER programs_lifecycle BEFORE UPDATE OR DELETE ON product.programs
FOR EACH ROW EXECUTE FUNCTION product.enforce_program_lifecycle();

CREATE FUNCTION product.enforce_cohort_lifecycle() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog,product AS $$
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'cohort deletion is forbidden'; END IF;
  IF OLD.status IN ('completed','archived') THEN RAISE EXCEPTION 'terminal cohort is immutable'; END IF;
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.program_id IS DISTINCT FROM OLD.program_id OR NEW.name IS DISTINCT FROM OLD.name
    OR NEW.starts_at IS DISTINCT FROM OLD.starts_at OR NEW.ends_at IS DISTINCT FROM OLD.ends_at
    OR NEW.created_at IS DISTINCT FROM OLD.created_at OR NEW.status IS DISTINCT FROM OLD.status
    OR NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at
  THEN RAISE EXCEPTION 'invalid cohort enrollment fence update'; END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER cohorts_lifecycle BEFORE UPDATE OR DELETE ON product.cohorts
FOR EACH ROW EXECUTE FUNCTION product.enforce_cohort_lifecycle();

CREATE FUNCTION product.enforce_enrollment_lifecycle() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog,product AS $$
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'enrollment deletion is forbidden'; END IF;
  IF OLD.status<>'active' OR NEW.status<>'left' OR NEW.left_at IS NULL
    OR NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.cohort_id IS DISTINCT FROM OLD.cohort_id OR NEW.user_id IS DISTINCT FROM OLD.user_id
    OR NEW.enrolled_at IS DISTINCT FROM OLD.enrolled_at OR NEW.created_at IS DISTINCT FROM OLD.created_at
    OR NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at
  THEN RAISE EXCEPTION 'invalid enrollment transition'; END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER enrollments_lifecycle BEFORE UPDATE OR DELETE ON product.enrollments
FOR EACH ROW EXECUTE FUNCTION product.enforce_enrollment_lifecycle();
CREATE TRIGGER role_packs_append_only BEFORE UPDATE OR DELETE ON product.role_packs
FOR EACH ROW EXECUTE FUNCTION agent.reject_append_only_mutation();

CREATE INDEX cohorts_program_status_idx ON product.cohorts(tenant_id,program_id,status,created_at,id);
CREATE INDEX enrollments_cohort_status_idx ON product.enrollments(tenant_id,cohort_id,status,user_id);
CREATE INDEX role_packs_program_revision_idx ON product.role_packs(tenant_id,program_id,revision DESC);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service') THEN
    GRANT SELECT,INSERT,UPDATE ON product.programs,product.cohorts,product.enrollments TO lites_product_service;
    GRANT SELECT,INSERT ON product.role_packs TO lites_product_service;
    GRANT SELECT ON product.role_profiles,product.task_templates TO lites_product_service;
  END IF;
END
$$;
