DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid='product.missions'::regclass
      AND conname='missions_target_role_tenant_fk' AND convalidated
  ) OR NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid='product.missions'::regclass
      AND conname='missions_source_role_tenant_fk' AND convalidated
  ) OR NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid='product.capability_claims'::regclass
      AND conname='capability_claims_capability_tenant_fk' AND convalidated
  ) OR NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid='product.role_capability_requirements'::regclass
      AND conname='role_capability_requirements_role_tenant_fk' AND convalidated
  ) OR EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_identity_service') AND (
    NOT has_table_privilege('lites_identity_service','product.role_profiles','SELECT,INSERT')
    OR NOT has_table_privilege('lites_identity_service','product.capabilities','SELECT,INSERT')
    OR NOT has_table_privilege('lites_identity_service','product.role_capability_requirements','SELECT,INSERT')
    OR NOT has_table_privilege('lites_identity_service','product.rubric_versions','SELECT,INSERT')
  ) THEN
    RAISE EXCEPTION 'Claim content tenant-integrity boundary is incomplete';
  END IF;
END
$$;
