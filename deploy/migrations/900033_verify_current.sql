\set ON_ERROR_STOP on
BEGIN;
DO $verify$
DECLARE definition text;
BEGIN
  IF (SELECT count(*) FROM public.lites_schema_migrations)<>33 OR NOT EXISTS (
    SELECT 1 FROM public.lites_schema_migrations
    WHERE version=33 AND name='runtime_epoch_authorization'
  ) THEN RAISE EXCEPTION 'expected migration version 33'; END IF;
  SELECT pg_get_functiondef('agent.runtime_lock_execution_right(uuid,uuid,uuid,bigint,timestamptz)'::regprocedure) INTO definition;
  IF position('session_epoch' IN definition)=0
    OR position('e.store_epoch=session_epoch' IN definition)=0
    OR position('le.store_epoch=session_epoch' IN definition)=0
  THEN RAISE EXCEPTION 'runtime authorization is not store-epoch bound'; END IF;
END
$verify$;
ROLLBACK;
SELECT json_build_object('status','passed','schema_version',33,'runtime_authorization','store_epoch_bound') AS current_schema_verification;
