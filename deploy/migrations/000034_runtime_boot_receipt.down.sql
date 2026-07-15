DROP TRIGGER IF EXISTS runtime_boot_receipt_lifecycle ON agent.runtime_sessions;
DROP FUNCTION IF EXISTS agent.enforce_runtime_boot_receipt();
ALTER TABLE agent.runtime_sessions DROP CONSTRAINT IF EXISTS runtime_sessions_boot_receipt_contract;
ALTER TABLE agent.runtime_sessions DROP COLUMN IF EXISTS boot_receipt_hash;
