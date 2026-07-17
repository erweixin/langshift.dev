DO $$ BEGIN
  IF (SELECT max(version) FROM public.lites_schema_migrations)<>66 THEN RAISE EXCEPTION 'expected migration version 66'; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='evidence_plaintext_hash_contract') THEN RAISE EXCEPTION 'evidence plaintext integrity fence missing'; END IF;
END $$;
SELECT json_build_object('status','passed','schema_version',66,'evidence_integrity','ciphertext_manifest_plus_plaintext_digest') AS current_schema_verification;
