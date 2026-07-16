DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_runtime_service') THEN
    REVOKE EXECUTE ON FUNCTION agent.runtime_active_session_count() FROM lites_runtime_service;
  END IF;
END
$$;

DROP FUNCTION IF EXISTS agent.runtime_active_session_count();
