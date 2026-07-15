DROP FUNCTION IF EXISTS agent.list_recoverable_portfolio_tenants(uuid,uuid,integer,integer,integer,timestamptz,timestamptz);
DROP FUNCTION IF EXISTS agent.list_expirable_portfolio_tenants(uuid,uuid,integer,integer,integer,timestamptz);
DROP INDEX IF EXISTS product.portfolio_exports_expiry_idx;
DROP INDEX IF EXISTS product.portfolio_exports_recovery_idx;
DROP TRIGGER IF EXISTS portfolio_exports_lifecycle ON product.portfolio_exports;
DROP FUNCTION IF EXISTS product.enforce_portfolio_export_lifecycle();
DROP TABLE IF EXISTS product.portfolio_export_evidence;
DROP TABLE IF EXISTS product.portfolio_export_artifacts;

ALTER TABLE product.portfolio_exports
  DROP CONSTRAINT IF EXISTS portfolio_exports_completion_event_fk,
  DROP CONSTRAINT IF EXISTS portfolio_exports_request_event_fk,
  DROP CONSTRAINT IF EXISTS portfolio_exports_start_command_fk,
  DROP CONSTRAINT IF EXISTS portfolio_exports_run_fk,
  DROP CONSTRAINT IF EXISTS portfolio_exports_workspace_binding_fk,
  DROP CONSTRAINT IF EXISTS portfolio_exports_tenant_project_fk,
  DROP CONSTRAINT IF EXISTS portfolio_exports_lifecycle_contract,
  DROP CONSTRAINT IF EXISTS portfolio_exports_scope_contract,
  DROP CONSTRAINT IF EXISTS portfolio_exports_status_contract,
  DROP CONSTRAINT IF EXISTS portfolio_exports_start_command_unique,
  DROP CONSTRAINT IF EXISTS portfolio_exports_run_unique,
  DROP CONSTRAINT IF EXISTS portfolio_exports_request_unique,
  DROP CONSTRAINT IF EXISTS portfolio_exports_tenant_id_id_unique,
  DROP COLUMN IF EXISTS expired_at,
  DROP COLUMN IF EXISTS failure_code,
  DROP COLUMN IF EXISTS failed_at,
  DROP COLUMN IF EXISTS started_at,
  DROP COLUMN IF EXISTS scan_result_hash,
  DROP COLUMN IF EXISTS byte_size,
  DROP COLUMN IF EXISTS media_type,
  DROP COLUMN IF EXISTS object_version,
  DROP COLUMN IF EXISTS completion_event_id,
  DROP COLUMN IF EXISTS request_event_id,
  DROP COLUMN IF EXISTS start_command_id,
  DROP COLUMN IF EXISTS run_id,
  DROP COLUMN IF EXISTS revision_manifest_hash,
  DROP COLUMN IF EXISTS export_format,
  DROP COLUMN IF EXISTS workspace_manifest_hash,
  DROP COLUMN IF EXISTS workspace_revision,
  DROP COLUMN IF EXISTS workspace_binding_version,
  DROP COLUMN IF EXISTS workspace_binding_id,
  DROP COLUMN IF EXISTS project_version,
  DROP COLUMN IF EXISTS request_id;

ALTER TABLE product.artifact_revisions DROP CONSTRAINT IF EXISTS artifact_revisions_exact_binding_unique;
ALTER TABLE product.project_workspace_bindings
  DROP CONSTRAINT IF EXISTS project_workspace_bindings_exact_head_unique,
  DROP CONSTRAINT IF EXISTS project_workspace_bindings_tenant_id_id_unique;
