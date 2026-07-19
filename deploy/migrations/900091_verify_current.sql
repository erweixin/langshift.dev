DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service') AND (
    NOT has_table_privilege('lites_product_service','product.aggregate_metrics','INSERT')
    OR NOT has_table_privilege('lites_product_service','product.aggregate_snapshots','INSERT')
    OR NOT has_table_privilege('lites_product_service','contracts.usage_ledger','SELECT')
    OR NOT has_schema_privilege('lites_product_service','contracts','USAGE')
  ) THEN
    RAISE EXCEPTION 'aggregate snapshot service grants are incomplete';
  END IF;
END
$$;
