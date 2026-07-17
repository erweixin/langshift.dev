CREATE FUNCTION agent.lock_owned_route_planning_mission(
  p_tenant_id uuid,
  p_user_id uuid,
  p_mission_id uuid
) RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, product
SET row_security = off
AS $$
BEGIN
  IF p_tenant_id IS NULL OR p_user_id IS NULL OR p_mission_id IS NULL THEN
    RAISE EXCEPTION 'route planning mission lock scope is required' USING ERRCODE='22023';
  END IF;
  PERFORM 1
  FROM product.missions
  WHERE tenant_id=p_tenant_id AND user_id=p_user_id AND id=p_mission_id
    AND status IN ('draft','active')
  FOR KEY SHARE;
  RETURN FOUND;
END
$$;

REVOKE ALL ON FUNCTION agent.lock_owned_route_planning_mission(uuid,uuid,uuid) FROM PUBLIC;

CREATE UNIQUE INDEX route_revisions_planner_run_unique
  ON product.route_revisions(tenant_id,planner_run_id)
  WHERE planner_run_id IS NOT NULL;

ALTER TABLE product.route_revisions
  ADD CONSTRAINT route_revisions_planner_run_fk
  FOREIGN KEY (tenant_id,planner_run_id)
  REFERENCES agent.runs(tenant_id,id)
  ON DELETE RESTRICT
  DEFERRABLE INITIALLY DEFERRED;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service') THEN
    GRANT SELECT,INSERT,UPDATE ON agent.conversations TO lites_product_service;
    GRANT SELECT,INSERT ON agent.run_messages TO lites_product_service;
    GRANT EXECUTE ON FUNCTION agent.lock_owned_route_planning_mission(uuid,uuid,uuid) TO lites_product_service;
  END IF;
END
$$;
