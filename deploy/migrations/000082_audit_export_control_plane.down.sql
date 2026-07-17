BEGIN;
DROP TABLE contracts.audit_exports;
ALTER TABLE contracts.admin_access_audit DROP CONSTRAINT admin_access_audit_scope;
ALTER TABLE contracts.admin_access_audit ADD CONSTRAINT admin_access_audit_scope CHECK (
  action IN ('usage_snapshot_read','audit_export_read')
  AND resource_kind IN ('usage_snapshot','audit_export')
  AND length(btrim(request_id)) BETWEEN 1 AND 256
  AND length(btrim(reason)) BETWEEN 1 AND 500
  AND reason_hash ~ '^[0-9a-f]{64}$'
);
COMMIT;
