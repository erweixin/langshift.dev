BEGIN;

-- Public account mutations remain limited to en and zh-CN. The additional
-- value is an internal, non-identifying tombstone used only after erasure.
ALTER TABLE identity.users
  DROP CONSTRAINT IF EXISTS users_locale_check,
  DROP CONSTRAINT IF EXISTS users_locale_supported,
  ADD CONSTRAINT users_locale_supported CHECK (locale IN ('en','zh-CN','und'));

COMMIT;
