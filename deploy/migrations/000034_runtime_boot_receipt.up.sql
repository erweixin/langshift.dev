ALTER TABLE agent.runtime_sessions
  ADD COLUMN boot_receipt_hash text,
  ADD CONSTRAINT runtime_sessions_boot_receipt_contract CHECK (
    (ready_at IS NULL AND boot_receipt_hash IS NULL)
    OR (ready_at IS NOT NULL AND boot_receipt_hash ~ '^[0-9a-f]{64}$')
  );

CREATE FUNCTION agent.enforce_runtime_boot_receipt() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.boot_receipt_hash IS NOT NULL AND NEW.boot_receipt_hash IS DISTINCT FROM OLD.boot_receipt_hash THEN
    RAISE EXCEPTION 'runtime boot receipt mutation is forbidden';
  END IF;
  IF OLD.boot_receipt_hash IS NULL AND NEW.boot_receipt_hash IS NOT NULL
    AND NOT (OLD.status='provisioning' AND NEW.status='ready')
  THEN RAISE EXCEPTION 'runtime boot receipt may only be bound at readiness'; END IF;
  RETURN NEW;
END
$$;

CREATE TRIGGER runtime_boot_receipt_lifecycle BEFORE UPDATE ON agent.runtime_sessions
  FOR EACH ROW EXECUTE FUNCTION agent.enforce_runtime_boot_receipt();
