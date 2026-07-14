\set ON_ERROR_STOP on
BEGIN;
DO $verify$
DECLARE actual integer; lifecycle text; contract text;
BEGIN
  SELECT count(*) INTO actual FROM public.lites_schema_migrations;
  IF actual<>21 OR NOT EXISTS (SELECT 1 FROM public.lites_schema_migrations WHERE version=21 AND name='workspace_prepared_proposal') THEN RAISE EXCEPTION 'expected migration version 21'; END IF;
  SELECT pg_get_constraintdef(oid) INTO contract FROM pg_constraint WHERE conrelid='agent.workspace_revision_commits'::regclass AND conname='workspace_revision_authorization_contract';
  IF position('NULLIF(proposal_hash' IN contract)=0 THEN RAISE EXCEPTION 'prepared proposal hash is not required'; END IF;
  SELECT pg_get_functiondef('agent.enforce_workspace_revision_lifecycle()'::regprocedure) INTO lifecycle;
  IF position('NEW.proposal_hash IS DISTINCT FROM OLD.proposal_hash' IN lifecycle)=0 THEN RAISE EXCEPTION 'prepared proposal hash is not immutable'; END IF;
END
$verify$;
ROLLBACK;
SELECT json_build_object('status','passed','schema_version',21,'workspace_prepared_proposal','content_bound_before_approval') AS current_schema_verification;
