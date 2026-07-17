DO $$ BEGIN
  IF (SELECT max(version) FROM public.lites_schema_migrations)<>68 THEN RAISE EXCEPTION 'expected migration version 68'; END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
    WHERE n.nspname='agent' AND p.proname='lock_active_focused_mission'
  ) THEN RAISE EXCEPTION 'Coach Focus admission fence missing'; END IF;
END $$;
SELECT json_build_object('status','passed','schema_version',68,'coach','focus_locked_at_conversation_and_message_admission') AS current_schema_verification;
