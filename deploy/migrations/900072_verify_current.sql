DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
    WHERE n.nspname='agent' AND p.proname='lock_owned_portfolio_export'
  ) THEN
    RAISE EXCEPTION 'portfolio builder admission protocol is incomplete';
  END IF;
END
$$;
