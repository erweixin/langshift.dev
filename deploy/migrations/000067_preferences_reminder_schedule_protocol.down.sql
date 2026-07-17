DO $$ BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service') THEN
    REVOKE EXECUTE ON FUNCTION agent.list_due_reminder_schedule_tenants(uuid,integer) FROM lites_product_service;
  END IF;
END $$;
DROP FUNCTION IF EXISTS agent.list_due_reminder_schedule_tenants(uuid,integer);
ALTER TABLE product.reminder_deliveries
  DROP CONSTRAINT IF EXISTS reminder_deliveries_contract,
  DROP CONSTRAINT IF EXISTS reminder_deliveries_schedule_fk;
DROP INDEX IF EXISTS product.reminder_schedules_one_live_kind;
ALTER TABLE product.reminder_schedules
  DROP CONSTRAINT IF EXISTS reminder_schedules_lifecycle_contract,
  DROP CONSTRAINT IF EXISTS reminder_schedules_contract,
  DROP CONSTRAINT IF EXISTS reminder_schedules_tenant_id_id_unique,
  DROP COLUMN IF EXISTS channel,
  DROP COLUMN IF EXISTS weekdays,
  DROP COLUMN IF EXISTS local_time,
  DROP COLUMN IF EXISTS schedule_kind;
ALTER TABLE product.user_preferences DROP CONSTRAINT IF EXISTS user_preferences_contract;
