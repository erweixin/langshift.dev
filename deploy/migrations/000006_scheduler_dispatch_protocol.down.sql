DROP FUNCTION IF EXISTS agent.scheduler_abort_resource(text,text,bigint,bytea,timestamptz);
DROP FUNCTION IF EXISTS agent.scheduler_commit_dispatch(text,text,bigint,bytea,jsonb,jsonb,timestamptz,timestamptz);
DROP FUNCTION IF EXISTS agent.scheduler_list_ready_jobs(text,text,bigint,bytea,uuid,timestamptz,integer);
DROP FUNCTION IF EXISTS agent.scheduler_claim_resource(text,text,bytea,timestamptz,timestamptz);
DROP TABLE IF EXISTS agent.scheduler_resources;
