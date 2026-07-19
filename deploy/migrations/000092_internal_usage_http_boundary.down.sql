BEGIN;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_contract_service') THEN
    REVOKE EXECUTE ON FUNCTION
      contracts.reserve_credit_units(uuid,uuid,bigint,timestamptz,timestamptz),
      contracts.settle_credit_units(uuid,uuid,bigint,bigint,timestamptz),
      contracts.release_credit_units(uuid,uuid,bigint,timestamptz)
      FROM lites_contract_service;
    REVOKE SELECT,INSERT ON contracts.usage_ledger FROM lites_contract_service;
    REVOKE SELECT,INSERT,UPDATE ON contracts.usage_reservations FROM lites_contract_service;
    REVOKE SELECT ON agent.llm_provider_attempts FROM lites_contract_service;
  END IF;
END
$$;

COMMIT;
