BEGIN;

UPDATE identity.users
SET locale='en'
WHERE locale='und' AND status='deleted';

ALTER TABLE identity.users
  DROP CONSTRAINT IF EXISTS users_locale_supported,
  ADD CONSTRAINT users_locale_supported CHECK (locale IN ('en','zh-CN'));

COMMIT;
