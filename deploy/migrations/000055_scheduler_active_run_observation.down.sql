DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_scheduler_service') THEN
    REVOKE EXECUTE ON FUNCTION agent.scheduler_active_run_count() FROM lites_scheduler_service;
  END IF;
END
$$;

DROP FUNCTION IF EXISTS agent.scheduler_active_run_count();
