BEGIN;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_identity_service') THEN
    REVOKE INSERT ON product.role_profiles FROM lites_identity_service;
    REVOKE SELECT,INSERT ON product.capabilities,product.role_capability_requirements,product.rubric_versions FROM lites_identity_service;
  END IF;
END
$$;

ALTER TABLE product.capability_claims
  DROP CONSTRAINT capability_claims_capability_tenant_fk,
  ADD CONSTRAINT capability_claims_capability_id_fk
    FOREIGN KEY (capability_id) REFERENCES product.capabilities(id) ON DELETE RESTRICT;

ALTER TABLE product.transition_templates
  DROP CONSTRAINT transition_templates_source_role_tenant_fk,
  DROP CONSTRAINT transition_templates_target_role_tenant_fk;

ALTER TABLE product.missions
  DROP CONSTRAINT missions_source_role_tenant_fk,
  DROP CONSTRAINT missions_target_role_tenant_fk,
  ADD CONSTRAINT missions_target_role_profile_id_fk
    FOREIGN KEY (target_role_profile_id) REFERENCES product.role_profiles(id) ON DELETE RESTRICT;

ALTER TABLE product.role_capability_requirements
  DROP CONSTRAINT role_capability_requirements_role_tenant_fk,
  DROP CONSTRAINT role_capability_requirements_capability_tenant_fk,
  ADD CONSTRAINT role_capability_requirements_role_profile_id_fk
    FOREIGN KEY (role_profile_id) REFERENCES product.role_profiles(id) ON DELETE CASCADE,
  ADD CONSTRAINT role_capability_requirements_capability_id_fk
    FOREIGN KEY (capability_id) REFERENCES product.capabilities(id) ON DELETE CASCADE;

ALTER TABLE product.capabilities DROP CONSTRAINT capabilities_tenant_id_unique;
ALTER TABLE product.role_profiles DROP CONSTRAINT role_profiles_tenant_id_unique;

COMMIT;
