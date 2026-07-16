CREATE FUNCTION agent.runtime_active_session_count()
RETURNS bigint
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, agent
SET row_security = off
AS $$
  SELECT count(*)::bigint
  FROM agent.runtime_sessions
  WHERE status IN ('requested','provisioning','ready','running','idle','termination_requested')
$$;

REVOKE ALL ON FUNCTION agent.runtime_active_session_count() FROM PUBLIC;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_runtime_service') THEN
    GRANT EXECUTE ON FUNCTION agent.runtime_active_session_count() TO lites_runtime_service;
  END IF;
END
$$;
