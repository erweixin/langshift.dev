DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_identity_service') THEN
    REVOKE SELECT,DELETE ON agent.conversations FROM lites_identity_service;
    REVOKE SELECT ON agent.run_messages,agent.run_message_chunks,agent.snapshots FROM lites_identity_service;
  END IF;
END
$$;
