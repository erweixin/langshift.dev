DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service') THEN
    REVOKE SELECT,INSERT,UPDATE ON product.programs,product.cohorts,product.enrollments FROM lites_product_service;
    REVOKE SELECT,INSERT ON product.role_packs FROM lites_product_service;
  END IF;
END
$$;

DROP INDEX product.role_packs_program_revision_idx;
DROP INDEX product.enrollments_cohort_status_idx;
DROP INDEX product.cohorts_program_status_idx;
DROP TRIGGER role_packs_append_only ON product.role_packs;
DROP TRIGGER enrollments_lifecycle ON product.enrollments;
DROP FUNCTION product.enforce_enrollment_lifecycle();
DROP TRIGGER cohorts_lifecycle ON product.cohorts;
DROP FUNCTION product.enforce_cohort_lifecycle();
DROP TRIGGER programs_lifecycle ON product.programs;
DROP FUNCTION product.enforce_program_lifecycle();

ALTER TABLE product.role_packs
  DROP CONSTRAINT role_packs_contract,
  DROP CONSTRAINT role_packs_tenant_program_fk,
  DROP CONSTRAINT role_packs_tenant_id_id_unique;
ALTER TABLE product.enrollments
  DROP CONSTRAINT enrollments_contract,
  DROP CONSTRAINT enrollments_member_fk,
  DROP CONSTRAINT enrollments_tenant_cohort_fk,
  DROP CONSTRAINT enrollments_tenant_id_id_unique;
ALTER TABLE product.cohorts
  DROP CONSTRAINT cohorts_contract,
  DROP CONSTRAINT cohorts_tenant_program_fk,
  DROP CONSTRAINT cohorts_tenant_id_id_unique;
ALTER TABLE product.programs
  DROP CONSTRAINT programs_contract,
  DROP CONSTRAINT programs_owner_membership_fk,
  DROP CONSTRAINT programs_tenant_id_id_unique;
