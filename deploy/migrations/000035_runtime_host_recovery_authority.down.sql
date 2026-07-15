DROP FUNCTION IF EXISTS agent.runtime_list_host_machines(uuid,text,bytea,uuid,integer);
DROP FUNCTION IF EXISTS agent.runtime_lock_due_session(uuid,uuid,uuid,bigint,timestamptz);
DROP FUNCTION IF EXISTS agent.runtime_lock_owned_machine(uuid,text,bytea,uuid,uuid,uuid,uuid,text,bigint);
