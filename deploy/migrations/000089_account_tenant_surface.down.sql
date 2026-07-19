BEGIN;

DROP FUNCTION IF EXISTS identity.list_user_tenants(uuid,uuid,uuid,timestamptz,uuid,integer);

ALTER TABLE identity.users
  DROP CONSTRAINT IF EXISTS users_locale_supported,
  DROP CONSTRAINT IF EXISTS users_timezone_length,
  DROP CONSTRAINT IF EXISTS users_display_name_length,
  DROP COLUMN IF EXISTS display_name,
  DROP COLUMN IF EXISTS timezone;

COMMIT;
