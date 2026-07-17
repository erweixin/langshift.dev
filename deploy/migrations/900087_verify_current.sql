DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service') AND (
    EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service' AND rolbypassrls)
    OR NOT has_table_privilege('lites_product_service','product.rubric_versions','SELECT,INSERT')
  ) THEN
    RAISE EXCEPTION 'product immutable rubric activation privilege contract is incomplete';
  END IF;
END
$$;

SELECT json_build_object('status','passed','schema_version',87,'product_content_rubrics','tenant_projected') AS current_schema_verification;
