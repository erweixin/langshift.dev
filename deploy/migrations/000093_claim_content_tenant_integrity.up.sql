BEGIN;

ALTER TABLE product.role_profiles
  ADD CONSTRAINT role_profiles_tenant_id_unique UNIQUE (tenant_id,id);
ALTER TABLE product.capabilities
  ADD CONSTRAINT capabilities_tenant_id_unique UNIQUE (tenant_id,id);

ALTER TABLE product.role_capability_requirements
  DROP CONSTRAINT role_capability_requirements_role_profile_id_fk,
  DROP CONSTRAINT role_capability_requirements_capability_id_fk,
  ADD CONSTRAINT role_capability_requirements_role_tenant_fk
    FOREIGN KEY (tenant_id,role_profile_id) REFERENCES product.role_profiles(tenant_id,id) ON DELETE CASCADE NOT VALID,
  ADD CONSTRAINT role_capability_requirements_capability_tenant_fk
    FOREIGN KEY (tenant_id,capability_id) REFERENCES product.capabilities(tenant_id,id) ON DELETE CASCADE NOT VALID;

ALTER TABLE product.missions
  DROP CONSTRAINT missions_target_role_profile_id_fk,
  ADD CONSTRAINT missions_source_role_tenant_fk
    FOREIGN KEY (tenant_id,source_role_profile_id) REFERENCES product.role_profiles(tenant_id,id) ON DELETE RESTRICT NOT VALID,
  ADD CONSTRAINT missions_target_role_tenant_fk
    FOREIGN KEY (tenant_id,target_role_profile_id) REFERENCES product.role_profiles(tenant_id,id) ON DELETE RESTRICT NOT VALID;

ALTER TABLE product.transition_templates
  ADD CONSTRAINT transition_templates_source_role_tenant_fk
    FOREIGN KEY (tenant_id,source_role_profile_id) REFERENCES product.role_profiles(tenant_id,id) ON DELETE RESTRICT NOT VALID,
  ADD CONSTRAINT transition_templates_target_role_tenant_fk
    FOREIGN KEY (tenant_id,target_role_profile_id) REFERENCES product.role_profiles(tenant_id,id) ON DELETE RESTRICT NOT VALID;

ALTER TABLE product.capability_claims
  DROP CONSTRAINT capability_claims_capability_id_fk,
  ADD CONSTRAINT capability_claims_capability_tenant_fk
    FOREIGN KEY (tenant_id,capability_id) REFERENCES product.capabilities(tenant_id,id) ON DELETE RESTRICT NOT VALID;

ALTER TABLE product.role_capability_requirements
  VALIDATE CONSTRAINT role_capability_requirements_role_tenant_fk;
ALTER TABLE product.role_capability_requirements
  VALIDATE CONSTRAINT role_capability_requirements_capability_tenant_fk;
ALTER TABLE product.missions VALIDATE CONSTRAINT missions_source_role_tenant_fk;
ALTER TABLE product.missions VALIDATE CONSTRAINT missions_target_role_tenant_fk;
ALTER TABLE product.transition_templates VALIDATE CONSTRAINT transition_templates_source_role_tenant_fk;
ALTER TABLE product.transition_templates VALIDATE CONSTRAINT transition_templates_target_role_tenant_fk;
ALTER TABLE product.capability_claims VALIDATE CONSTRAINT capability_claims_capability_tenant_fk;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_identity_service') THEN
    GRANT SELECT,INSERT ON product.role_profiles,product.capabilities,
      product.role_capability_requirements,product.rubric_versions
      TO lites_identity_service;
  END IF;
END
$$;

COMMIT;
