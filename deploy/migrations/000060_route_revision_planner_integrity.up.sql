ALTER TABLE product.route_revisions
  ADD COLUMN route_payload_hash text,
  ADD COLUMN planner_command_id uuid,
  ADD COLUMN planner_run_id uuid;

ALTER TABLE product.route_revisions
  ADD CONSTRAINT route_revisions_payload_integrity
  CHECK (
    (route_payload_ref IS NULL AND route_payload_hash IS NULL)
    OR
    (route_payload_ref IS NOT NULL AND route_payload_hash ~ '^[0-9a-f]{64}$')
  ) NOT VALID;

CREATE UNIQUE INDEX route_revisions_planner_command_unique
  ON product.route_revisions(planner_command_id)
  WHERE planner_command_id IS NOT NULL;

CREATE INDEX route_revisions_owner_created_idx
  ON product.route_revisions(tenant_id,user_id,created_at DESC,id DESC);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service') THEN
    GRANT SELECT,INSERT,UPDATE ON product.route_revisions TO lites_product_service;
  END IF;
END
$$;
