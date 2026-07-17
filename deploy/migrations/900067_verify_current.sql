DO $$ BEGIN
  IF (SELECT max(version) FROM public.lites_schema_migrations)<>67 THEN RAISE EXCEPTION 'expected migration version 67'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='reminder_schedules_lifecycle_contract') THEN RAISE EXCEPTION 'reminder lifecycle fence missing'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_proc WHERE proname='list_due_reminder_schedule_tenants') THEN RAISE EXCEPTION 'due reminder discovery missing'; END IF;
END $$;
SELECT json_build_object('status','passed','schema_version',67,'preferences','closed_coach_contract','reminders','dst_aware_schedule_and_delivery_ledger') AS current_schema_verification;
