\set ON_ERROR_STOP on
BEGIN;
DO $verify$
DECLARE actual integer; lifecycle text;
BEGIN
  SELECT count(*) INTO actual FROM public.lites_schema_migrations;
  IF actual<>22 OR NOT EXISTS (SELECT 1 FROM public.lites_schema_migrations WHERE version=22 AND name='workspace_publish_heartbeat') THEN RAISE EXCEPTION 'expected migration version 22'; END IF;
  SELECT pg_get_functiondef('agent.enforce_workspace_revision_lifecycle()'::regprocedure) INTO lifecycle;
  IF position($needle$OLD.status='publishing' AND NEW.status='publishing'$needle$ IN lifecycle)=0 THEN RAISE EXCEPTION 'workspace publish heartbeat transition is absent'; END IF;
  IF position($needle$NEW.publish_fence<=OLD.publish_fence$needle$ IN lifecycle)=0 THEN RAISE EXCEPTION 'workspace publish fence is not monotonic'; END IF;
  IF position('NEW.commit_command_id IS DISTINCT FROM OLD.commit_command_id' IN lifecycle)=0 THEN RAISE EXCEPTION 'workspace commit command is mutable'; END IF;
END
$verify$;
ROLLBACK;
SELECT json_build_object('status','passed','schema_version',22,'workspace_publish_heartbeat','fenced_and_lease_coherent') AS current_schema_verification;
