ALTER TABLE product.missions
  DROP CONSTRAINT IF EXISTS missions_goal_payload_integrity;

ALTER TABLE product.missions
  DROP COLUMN IF EXISTS goal_payload_hash;
