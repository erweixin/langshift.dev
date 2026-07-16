DO $$
DECLARE
  function_definition text;
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM public.lites_schema_migrations
    WHERE version=55 AND name='scheduler_active_run_observation'
  ) THEN
    RAISE EXCEPTION 'scheduler active run observation migration is not installed';
  END IF;

  SELECT pg_get_functiondef('agent.scheduler_active_run_count()'::regprocedure)
    INTO function_definition;
  IF position('waiting_tool' IN function_definition)=0
    OR position('waiting_child' IN function_definition)=0
    OR position('waiting_approval' IN function_definition)=0
    OR position('SET row_security TO ''off''' IN function_definition)=0 THEN
    RAISE EXCEPTION 'active run observation does not cover every non-terminal run state';
  END IF;

  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_scheduler_service')
    AND NOT has_function_privilege('lites_scheduler_service','agent.scheduler_active_run_count()','EXECUTE') THEN
    RAISE EXCEPTION 'scheduler service cannot execute active run observation';
  END IF;
END
$$;

SELECT json_build_object(
  'status','passed',
  'schema_version',55,
  'active_run_observation','database_authoritative_all_non_terminal_states'
) AS current_schema_verification;
