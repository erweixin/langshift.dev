DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_contract_service') AND (
    NOT has_table_privilege('lites_contract_service','agent.llm_provider_attempts','SELECT')
    OR NOT has_table_privilege('lites_contract_service','contracts.usage_reservations','SELECT,INSERT,UPDATE')
    OR NOT has_table_privilege('lites_contract_service','contracts.usage_ledger','SELECT,INSERT')
    OR NOT has_function_privilege('lites_contract_service','contracts.reserve_credit_units(uuid,uuid,bigint,timestamptz,timestamptz)','EXECUTE')
    OR NOT has_function_privilege('lites_contract_service','contracts.settle_credit_units(uuid,uuid,bigint,bigint,timestamptz)','EXECUTE')
    OR NOT has_function_privilege('lites_contract_service','contracts.release_credit_units(uuid,uuid,bigint,timestamptz)','EXECUTE')
  ) THEN
    RAISE EXCEPTION 'internal usage HTTP boundary grants are incomplete';
  END IF;
END
$$;
