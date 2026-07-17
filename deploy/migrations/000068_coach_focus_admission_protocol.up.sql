CREATE FUNCTION agent.lock_active_focused_mission(
  p_tenant_id uuid,
  p_user_id uuid,
  p_mission_id uuid
) RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,product
SET row_security=off
AS $$
DECLARE
  v_focused_mission_id uuid;
BEGIN
  IF p_tenant_id IS NULL OR p_user_id IS NULL OR p_mission_id IS NULL THEN
    RAISE EXCEPTION 'focused mission lock scope is required' USING ERRCODE='22023';
  END IF;

  SELECT f.mission_id INTO v_focused_mission_id
  FROM product.mission_focuses f
  WHERE f.tenant_id=p_tenant_id AND f.user_id=p_user_id AND f.focus_version>0
  FOR SHARE;

  IF v_focused_mission_id IS DISTINCT FROM p_mission_id THEN
    RETURN false;
  END IF;

  PERFORM 1
  FROM product.missions m
  WHERE m.tenant_id=p_tenant_id AND m.user_id=p_user_id AND m.id=p_mission_id AND m.status='active'
  FOR KEY SHARE;
  RETURN FOUND;
END
$$;

REVOKE ALL ON FUNCTION agent.lock_active_focused_mission(uuid,uuid,uuid) FROM PUBLIC;

DO $$ BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_agent_control_service') THEN
    GRANT EXECUTE ON FUNCTION agent.lock_active_focused_mission(uuid,uuid,uuid) TO lites_agent_control_service;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_agent_service') THEN
    GRANT EXECUTE ON FUNCTION agent.lock_active_focused_mission(uuid,uuid,uuid) TO lites_agent_service;
  END IF;
END $$;
