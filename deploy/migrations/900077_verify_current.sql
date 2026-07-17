DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='contracts_control_plane_contract')
    OR NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='contract_approval_decision_contract')
    OR NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='seat_allocations_lifecycle_contract')
    OR NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='contract_approval_decisions_validate' AND NOT tgisinternal)
    OR NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='contract_approval_decisions_reject' AND NOT tgisinternal)
    OR NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='contract_proposals_validate' AND NOT tgisinternal)
    OR NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='memberships_contract_seat' AND NOT tgisinternal)
    OR NOT EXISTS (SELECT 1 FROM pg_proc WHERE oid='contracts.apply_approved_contract_proposal(uuid,uuid,uuid,uuid,timestamptz)'::regprocedure AND prosecdef)
  THEN RAISE EXCEPTION 'contract control plane is incomplete'; END IF;
END
$$;
SELECT json_build_object('status','passed','schema_version',77,'contract_control_plane','two_person_reauth_bound','seat_accounting','membership_synchronized') AS current_schema_verification;
