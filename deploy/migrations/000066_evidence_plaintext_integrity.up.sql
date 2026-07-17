ALTER TABLE product.evidence
  ADD COLUMN plaintext_hash text,
  ADD CONSTRAINT evidence_plaintext_hash_contract CHECK (plaintext_hash IS NULL OR plaintext_hash ~ '^[0-9a-f]{64}$');

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service') THEN
    GRANT SELECT,INSERT ON product.evidence TO lites_product_service;
  END IF;
END
$$;
