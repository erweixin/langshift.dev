DO $$
DECLARE
  function_definition text;
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM public.lites_schema_migrations
    WHERE version=56 AND name='runtime_active_session_observation'
  ) THEN
    RAISE EXCEPTION 'runtime active session observation migration is not installed';
  END IF;

  SELECT pg_get_functiondef('agent.runtime_active_session_count()'::regprocedure)
    INTO function_definition;
  IF position('requested' IN function_definition)=0
    OR position('provisioning' IN function_definition)=0
    OR position('termination_requested' IN function_definition)=0
    OR position('SET row_security TO ''off''' IN function_definition)=0 THEN
    RAISE EXCEPTION 'active runtime observation does not cover every non-terminal session state';
  END IF;

  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_runtime_service')
    AND NOT has_function_privilege('lites_runtime_service','agent.runtime_active_session_count()','EXECUTE') THEN
    RAISE EXCEPTION 'runtime service cannot execute active session observation';
  END IF;
END
$$;

SELECT json_build_object(
  'status','passed',
  'schema_version',56,
  'active_runtime_observation','database_authoritative_all_non_terminal_states'
) AS current_schema_verification;
