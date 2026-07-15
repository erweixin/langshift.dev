\set ON_ERROR_STOP on
BEGIN;
DO $verify$
DECLARE definition text;
BEGIN
  IF (SELECT count(*) FROM public.lites_schema_migrations)<>32 OR NOT EXISTS (
    SELECT 1 FROM public.lites_schema_migrations
    WHERE version=32 AND name='runtime_hostless_termination'
  ) THEN RAISE EXCEPTION 'expected migration version 32'; END IF;
  SELECT pg_get_functiondef('agent.validate_runtime_allocation_session()'::regprocedure) INTO definition;
  IF position('NEW.host_id IS NOT NULL' IN definition)=0
    OR position('NEW.host_id IS NULL' IN definition)=0
    OR position('termination_requested' IN definition)=0
    OR position('hostless firecracker session has invalid state' IN definition)=0
  THEN RAISE EXCEPTION 'hostless runtime termination contract missing'; END IF;
END
$verify$;
ROLLBACK;
SELECT json_build_object('status','passed','schema_version',32,'runtime_hostless_termination','event_bound_without_phantom_allocation') AS current_schema_verification;
