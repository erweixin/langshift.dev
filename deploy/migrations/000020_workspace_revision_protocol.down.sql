DROP FUNCTION IF EXISTS agent.list_workspace_recovery_tenants(uuid,uuid,integer,integer,integer,timestamptz);
DROP INDEX IF EXISTS agent.workspace_revision_recovery_idx;
DROP TRIGGER IF EXISTS workspace_revision_lifecycle ON agent.workspace_revision_commits;
DROP FUNCTION IF EXISTS agent.enforce_workspace_revision_lifecycle();

ALTER TABLE agent.workspace_revision_commits
  DROP CONSTRAINT IF EXISTS workspace_revision_authorization_event_fk,
  DROP CONSTRAINT IF EXISTS workspace_revision_approval_event_fk,
  DROP CONSTRAINT IF EXISTS workspace_revision_approval_fk,
  DROP CONSTRAINT IF EXISTS workspace_revision_terminal_contract,
  DROP CONSTRAINT IF EXISTS workspace_revision_publish_contract,
  DROP CONSTRAINT IF EXISTS workspace_revision_authorization_contract,
  DROP CONSTRAINT IF EXISTS workspace_revision_scope_contract,
  DROP CONSTRAINT IF EXISTS workspace_revision_status_contract,
  DROP CONSTRAINT IF EXISTS workspace_revision_command_unique,
  DROP CONSTRAINT IF EXISTS workspace_revision_tenant_id_id_unique,
  DROP COLUMN IF EXISTS abandoned_at,
  DROP COLUMN IF EXISTS failed_at,
  DROP COLUMN IF EXISTS reconciliation_attempts,
  DROP COLUMN IF EXISTS reconciliation_due_at,
  DROP COLUMN IF EXISTS outcome_unknown_at,
  DROP COLUMN IF EXISTS publishing_at,
  DROP COLUMN IF EXISTS authorized_at,
  DROP COLUMN IF EXISTS publish_lease_expires_at,
  DROP COLUMN IF EXISTS publish_lease_hash,
  DROP COLUMN IF EXISTS publish_fence,
  DROP COLUMN IF EXISTS publish_attempt_id,
  DROP COLUMN IF EXISTS observed_revision,
  DROP COLUMN IF EXISTS published_hash,
  DROP COLUMN IF EXISTS commit_command_id,
  DROP COLUMN IF EXISTS proposal_hash,
  DROP COLUMN IF EXISTS approval_event_id,
  DROP COLUMN IF EXISTS approval_version,
  DROP COLUMN IF EXISTS approval_id,
  ADD CONSTRAINT workspace_revision_status_contract CHECK (status IN ('prepared','authorized','publishing','confirmed','outcome_unknown','failed','abandoned')),
  ADD CONSTRAINT workspace_revision_confirmation_contract CHECK ((status='confirmed' AND published_revision IS NOT NULL AND confirmed_at IS NOT NULL) OR (status<>'confirmed' AND confirmed_at IS NULL));
