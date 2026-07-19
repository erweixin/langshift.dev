DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_identity_service') AND (
    EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_identity_service' AND rolbypassrls)
    OR NOT has_table_privilege('lites_identity_service','product.mission_focuses','SELECT,INSERT,UPDATE')
    OR has_table_privilege('lites_identity_service','product.mission_focuses','DELETE,TRUNCATE,REFERENCES,TRIGGER')
  ) THEN
    RAISE EXCEPTION 'identity service anonymous Claim focus privileges are invalid';
  END IF;
END
$$;

SELECT json_build_object(
  'status', 'passed',
  'schema_version', 96,
  'contract', 'anonymous_claim_focus_privilege'
) AS current_schema_verification;
