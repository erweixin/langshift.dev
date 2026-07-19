DO $$
DECLARE
  definition text;
BEGIN
  SELECT pg_get_constraintdef(oid)
  INTO definition
  FROM pg_constraint
  WHERE conrelid='identity.users'::regclass AND conname='users_locale_supported';

  IF definition IS NULL OR position('und' in definition)=0 THEN
    RAISE EXCEPTION 'identity.users erasure locale sentinel is not enforced';
  END IF;
  IF EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid='identity.users'::regclass AND conname='users_locale_check'
  ) THEN
    RAISE EXCEPTION 'legacy identity.users locale constraint remains active';
  END IF;
END
$$;

SELECT json_build_object(
  'status', 'passed',
  'schema_version', 99,
  'contract', 'erased_user_locale_sentinel'
) AS current_schema_verification;
