BEGIN;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service') THEN
    REVOKE SELECT ON identity.sessions FROM lites_product_service;
    REVOKE SELECT,INSERT,UPDATE ON product.byok_credentials,product.memory_policies FROM lites_product_service;
    REVOKE SELECT,INSERT ON product.byok_credential_versions,product.settings_access_audit FROM lites_product_service;
  END IF;
END
$$;

DROP TABLE IF EXISTS product.settings_access_audit;
ALTER TABLE product.memory_policies DROP CONSTRAINT IF EXISTS memory_policies_lifecycle_contract;
ALTER TABLE product.byok_credentials DROP CONSTRAINT IF EXISTS byok_credentials_secret_hint_contract;
ALTER TABLE product.byok_credentials DROP COLUMN IF EXISTS secret_hint;

COMMIT;
