ALTER TABLE product.evidence
  DROP CONSTRAINT IF EXISTS evidence_plaintext_hash_contract,
  DROP COLUMN IF EXISTS plaintext_hash;
