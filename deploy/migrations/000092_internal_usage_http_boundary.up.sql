BEGIN;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_contract_service') THEN
    GRANT SELECT ON agent.llm_provider_attempts TO lites_contract_service;
    GRANT SELECT,INSERT,UPDATE ON contracts.usage_reservations TO lites_contract_service;
    GRANT SELECT,INSERT ON contracts.usage_ledger TO lites_contract_service;
    GRANT EXECUTE ON FUNCTION
      contracts.reserve_credit_units(uuid,uuid,bigint,timestamptz,timestamptz),
      contracts.settle_credit_units(uuid,uuid,bigint,bigint,timestamptz),
      contracts.release_credit_units(uuid,uuid,bigint,timestamptz)
      TO lites_contract_service;
  END IF;
END
$$;

COMMIT;
