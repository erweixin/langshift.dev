DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_identity_service') THEN
    GRANT SELECT,DELETE ON agent.conversations TO lites_identity_service;
    GRANT SELECT ON agent.run_messages,agent.run_message_chunks,agent.snapshots TO lites_identity_service;
  END IF;
END
$$;
