\set ON_ERROR_STOP on
BEGIN;
DO $verify$
DECLARE
  digest text := 'sha256:' || repeat('a',64);
  control bytea := decode(repeat('ab',32),'hex');
  observed timestamptz := '2026-07-15T12:00:00Z';
  host_version bigint;
BEGIN
  IF (SELECT count(*) FROM public.lites_schema_migrations)<>36 OR NOT EXISTS (
    SELECT 1 FROM public.lites_schema_migrations WHERE version=36 AND name='runtime_host_restart_recovery'
  ) THEN RAISE EXCEPTION 'expected migration version 36'; END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_proc
    WHERE oid='agent.runtime_register_host(text,text,text,text,text,text,text,bytea,integer,integer,integer,integer,timestamptz,timestamptz)'::regprocedure
      AND prosecdef AND proconfig @> ARRAY['row_security=off']
  ) OR has_function_privilege('public','agent.runtime_register_host(text,text,text,text,text,text,text,bytea,integer,integer,integer,integer,timestamptz,timestamptz)','EXECUTE')
  THEN RAISE EXCEPTION 'runtime host registration authority is not fenced'; END IF;

  host_version := agent.runtime_register_host('migration-restart-host','runtime-untrusted','x86_64','zone-a',digest,digest,digest,control,2,512,4096,1,observed,observed+interval '30 seconds');
  host_version := agent.runtime_set_host_status('migration-restart-host',host_version,control,'active',observed+interval '1 second',observed+interval '31 seconds');
  host_version := agent.runtime_set_host_status('migration-restart-host',host_version,control,'draining',observed+interval '2 seconds',observed+interval '32 seconds');
  host_version := agent.runtime_register_host('migration-restart-host','runtime-untrusted','x86_64','zone-a',digest,digest,digest,control,2,512,4096,1,observed+interval '3 seconds',observed+interval '33 seconds');
  IF host_version<>4 OR NOT EXISTS (SELECT 1 FROM agent.runtime_hosts WHERE host_id='migration-restart-host' AND status='recovering' AND version=4)
  THEN RAISE EXCEPTION 'draining runtime host cannot re-enter authenticated recovery'; END IF;
END
$verify$;
ROLLBACK;
SELECT json_build_object('status','passed','schema_version',36,'runtime_host_restart','draining_to_recovering') AS current_schema_verification;
