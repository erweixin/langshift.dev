ALTER TABLE product.missions
  ADD COLUMN goal_payload_hash text;

ALTER TABLE product.missions
  ADD CONSTRAINT missions_goal_payload_integrity
  CHECK (
    (goal_payload_ref IS NULL AND goal_payload_hash IS NULL)
    OR
    (goal_payload_ref IS NOT NULL AND goal_payload_hash ~ '^[0-9a-f]{64}$')
  ) NOT VALID;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service') THEN
    GRANT SELECT,INSERT,UPDATE ON product.missions TO lites_product_service;
  END IF;
END
$$;
