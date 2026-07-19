DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_identity_service') AND (
    EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_identity_service' AND rolbypassrls)
    OR NOT has_table_privilege('lites_identity_service','agent.conversations','SELECT,DELETE')
    OR has_table_privilege('lites_identity_service','agent.conversations','INSERT,UPDATE,TRUNCATE,REFERENCES,TRIGGER')
    OR NOT has_table_privilege('lites_identity_service','agent.run_messages','SELECT')
    OR has_table_privilege('lites_identity_service','agent.run_messages','INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')
    OR NOT has_table_privilege('lites_identity_service','agent.run_message_chunks','SELECT')
    OR has_table_privilege('lites_identity_service','agent.run_message_chunks','INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')
    OR NOT has_table_privilege('lites_identity_service','agent.snapshots','SELECT')
    OR has_table_privilege('lites_identity_service','agent.snapshots','INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')
  ) THEN
    RAISE EXCEPTION 'identity service anonymous execution erasure privileges are invalid';
  END IF;
END
$$;

SELECT json_build_object(
  'status', 'passed',
  'schema_version', 97,
  'contract', 'anonymous_claim_execution_erasure'
) AS current_schema_verification;
