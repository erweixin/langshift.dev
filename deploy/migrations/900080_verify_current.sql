DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='entitlement_proposals_contract')
    OR NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='entitlement_approval_contract')
    OR NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='contract_entitlements_append' AND NOT tgisinternal)
    OR NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='entitlement_proposals_validate' AND NOT tgisinternal)
    OR NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='entitlement_approvals_validate' AND NOT tgisinternal)
    OR NOT EXISTS (SELECT 1 FROM pg_proc WHERE oid='contracts.apply_approved_entitlement_proposal(uuid,uuid,uuid,uuid,uuid,timestamptz)'::regprocedure AND prosecdef)
  THEN RAISE EXCEPTION 'entitlement control plane is incomplete'; END IF;
END
$$;
SELECT json_build_object('status','passed','schema_version',80,'entitlements','two_person_reauth_bound','direct_mutation','forbidden') AS current_schema_verification;
