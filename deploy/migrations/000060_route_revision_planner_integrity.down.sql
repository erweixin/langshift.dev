DROP INDEX IF EXISTS product.route_revisions_owner_created_idx;
DROP INDEX IF EXISTS product.route_revisions_planner_command_unique;

ALTER TABLE product.route_revisions
  DROP CONSTRAINT IF EXISTS route_revisions_payload_integrity,
  DROP COLUMN IF EXISTS planner_run_id,
  DROP COLUMN IF EXISTS planner_command_id,
  DROP COLUMN IF EXISTS route_payload_hash;
