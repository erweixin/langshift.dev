DO $$ BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_agent_control_service') THEN
    REVOKE EXECUTE ON FUNCTION agent.lock_active_focused_mission(uuid,uuid,uuid) FROM lites_agent_control_service;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_agent_service') THEN
    REVOKE EXECUTE ON FUNCTION agent.lock_active_focused_mission(uuid,uuid,uuid) FROM lites_agent_service;
  END IF;
END $$;
DROP FUNCTION IF EXISTS agent.lock_active_focused_mission(uuid,uuid,uuid);
