DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='support_cases_contract')
    OR NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='support_messages_contract')
    OR NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='support_cases_lifecycle' AND NOT tgisinternal)
    OR NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='support_case_messages_append_only' AND NOT tgisinternal)
    OR NOT EXISTS (SELECT 1 FROM pg_proc WHERE oid='product.resolve_support_sla(uuid,text)'::regprocedure AND prosecdef)
  THEN RAISE EXCEPTION 'support case control plane is incomplete'; END IF;
END
$$;
SELECT json_build_object('status','passed','schema_version',81,'support_payloads','encrypted_external','sla','contract_snapshot') AS current_schema_verification;
