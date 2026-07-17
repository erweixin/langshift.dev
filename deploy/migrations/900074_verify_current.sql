DO $$
DECLARE actual integer;
BEGIN
  SELECT max(version) INTO actual FROM public.lites_schema_migrations;
  IF actual<>74 OR NOT EXISTS (SELECT 1 FROM public.lites_schema_migrations WHERE version=74 AND name='portfolio_export_finalization') THEN
    RAISE EXCEPTION 'expected migration version 74';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_proc
    WHERE oid='agent.list_portfolio_export_finalization_tenants(uuid,uuid,integer)'::regprocedure
      AND prosecdef
  ) THEN
    RAISE EXCEPTION 'portfolio export finalization discovery is incomplete';
  END IF;
END
$$;

SELECT json_build_object('status','passed','schema_version',74,'portfolio_export_finalization','trusted_tool_receipt') AS current_schema_verification;
