DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service') AND (
    EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service' AND rolbypassrls)
    OR NOT has_table_privilege('lites_product_service','product.capabilities','SELECT,INSERT')
    OR has_table_privilege('lites_product_service','product.capabilities','UPDATE,DELETE')
    OR NOT has_table_privilege('lites_product_service','product.capability_claims','SELECT,INSERT')
    OR has_table_privilege('lites_product_service','product.capability_claims','UPDATE,DELETE')
    OR NOT has_table_privilege('lites_product_service','product.claim_evidence_links','SELECT,INSERT')
    OR has_table_privilege('lites_product_service','product.claim_evidence_links','UPDATE,DELETE')
    OR NOT has_table_privilege('lites_product_service','product.route_revisions','SELECT,UPDATE')
    OR NOT has_table_privilege('lites_product_service','product.missions','SELECT,UPDATE')
  ) THEN
    RAISE EXCEPTION 'capability claim protocol privilege contract is incomplete';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_trigger
    WHERE tgrelid='product.capability_claims'::regclass
      AND tgname='capability_claims_append_only'
      AND NOT tgisinternal
  ) OR NOT EXISTS (
    SELECT 1 FROM pg_trigger
    WHERE tgrelid='product.claim_evidence_links'::regclass
      AND tgname='claim_evidence_links_append_only'
      AND NOT tgisinternal
  ) THEN
    RAISE EXCEPTION 'capability claim append-only triggers are missing';
  END IF;
END
$$;

SELECT json_build_object('status','passed','schema_version',88,'capability_claims','append_only_revision_protocol') AS current_schema_verification;
