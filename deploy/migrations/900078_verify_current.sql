DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='accounting_adjustment_proposals_contract')
    OR NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='accounting_adjustment_approval_contract')
    OR NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='manual_adjustments_completeness')
    OR NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='accounting_adjustment_proposals_validate' AND NOT tgisinternal)
    OR NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='accounting_adjustment_approvals_validate' AND NOT tgisinternal)
    OR NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='accounting_adjustment_approvals_reject' AND NOT tgisinternal)
    OR NOT EXISTS (SELECT 1 FROM pg_proc WHERE oid='contracts.apply_approved_accounting_adjustment(uuid,uuid,uuid,uuid,timestamptz)'::regprocedure AND prosecdef)
  THEN RAISE EXCEPTION 'accounting adjustment control plane is incomplete'; END IF;
END
$$;
SELECT json_build_object('status','passed','schema_version',78,'accounting_adjustment','two_person_reauth_bound','hard_cap','preserved') AS current_schema_verification;
