DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service') THEN
    REVOKE EXECUTE ON FUNCTION agent.lock_owned_route_planning_mission(uuid,uuid,uuid) FROM lites_product_service;
    REVOKE SELECT,INSERT ON agent.run_messages FROM lites_product_service;
    REVOKE SELECT,INSERT,UPDATE ON agent.conversations FROM lites_product_service;
  END IF;
END
$$;

ALTER TABLE product.route_revisions
  DROP CONSTRAINT IF EXISTS route_revisions_planner_run_fk;

DROP INDEX IF EXISTS product.route_revisions_planner_run_unique;
DROP FUNCTION IF EXISTS agent.lock_owned_route_planning_mission(uuid,uuid,uuid);
