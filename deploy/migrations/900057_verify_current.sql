DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM public.lites_schema_migrations
    WHERE version=57 AND name='conversation_admission_protocol'
  ) THEN
    RAISE EXCEPTION 'conversation admission protocol migration is not installed';
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM pg_class c
    JOIN pg_namespace n ON n.oid=c.relnamespace
    WHERE n.nspname='agent' AND c.relname='conversations'
      AND c.relrowsecurity AND c.relforcerowsecurity
  ) THEN
    RAISE EXCEPTION 'conversation storage is missing forced tenant isolation';
  END IF;

  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='conversations_tenant_mission_fk')
    OR NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='conversations_tenant_last_run_fk' AND condeferrable AND condeferred)
    OR NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='conversations_title_contract')
    OR NOT EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname='agent' AND indexname='conversations_owner_updated_idx') THEN
    RAISE EXCEPTION 'conversation ownership, mission, run, or access contract is incomplete';
  END IF;

  IF to_regprocedure('agent.lock_active_owned_mission(uuid,uuid,uuid)') IS NULL
    OR position('SECURITY DEFINER' IN pg_get_functiondef('agent.lock_active_owned_mission(uuid,uuid,uuid)'::regprocedure))=0
    OR position('SET row_security TO ''off''' IN pg_get_functiondef('agent.lock_active_owned_mission(uuid,uuid,uuid)'::regprocedure))=0
    OR position('FOR KEY SHARE' IN pg_get_functiondef('agent.lock_active_owned_mission(uuid,uuid,uuid)'::regprocedure))=0 THEN
    RAISE EXCEPTION 'active mission lock is not narrow, privileged, and race-safe';
  END IF;

  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_agent_control_service')
    AND NOT has_table_privilege('lites_agent_control_service','agent.conversations','SELECT,INSERT,UPDATE') THEN
    RAISE EXCEPTION 'agent control service cannot operate conversations';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_agent_control_service')
    AND NOT has_function_privilege('lites_agent_control_service','agent.lock_active_owned_mission(uuid,uuid,uuid)','EXECUTE') THEN
    RAISE EXCEPTION 'agent control service cannot lock active owned missions';
  END IF;
END
$$;

SELECT json_build_object(
  'status','passed',
  'schema_version',57,
  'conversation_admission','tenant_owned_mission_bound_versioned'
) AS current_schema_verification;
