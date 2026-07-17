DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_agent_service') THEN
    REVOKE EXECUTE ON FUNCTION agent.lock_authorized_artifact_export(uuid,uuid,uuid,text,uuid,text) FROM lites_agent_service;
  END IF;
END
$$;

DROP FUNCTION agent.lock_authorized_artifact_export(uuid,uuid,uuid,text,uuid,text);
