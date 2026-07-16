DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_agent_control_service') THEN
    REVOKE SELECT,INSERT,UPDATE ON agent.conversations FROM lites_agent_control_service;
    REVOKE EXECUTE ON FUNCTION agent.lock_active_owned_mission(uuid,uuid,uuid) FROM lites_agent_control_service;
  END IF;
END
$$;

DROP FUNCTION IF EXISTS agent.lock_active_owned_mission(uuid,uuid,uuid);
DROP TABLE IF EXISTS agent.conversations;

ALTER TABLE product.missions
  DROP CONSTRAINT IF EXISTS missions_tenant_id_id_unique;
