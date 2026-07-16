CREATE FUNCTION agent.scheduler_active_run_count()
RETURNS bigint
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, agent
SET row_security = off
AS $$
  SELECT count(*)::bigint
  FROM agent.runs
  WHERE status IN ('accepted','queued','executing','waiting_tool','waiting_child','waiting_approval')
$$;

REVOKE ALL ON FUNCTION agent.scheduler_active_run_count() FROM PUBLIC;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_scheduler_service') THEN
    GRANT EXECUTE ON FUNCTION agent.scheduler_active_run_count() TO lites_scheduler_service;
  END IF;
END
$$;
